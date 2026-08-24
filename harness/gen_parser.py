"""Generate the parser's data tables from the pinned sqlglot reference.

    python harness/gen_parser.py --sqlglot ~/opensource/sqlglot

Writes sqlglot/parser_gen.go: which keyword tokens may stand in for an
identifier, which may stand in for a table alias, and which function names the
reference gives a node class of their own rather than parsing as Anonymous.

Same reasoning as the tokenizer tables -- these are data, hundreds of entries
per dialect, and transcribing them by hand would be a source of silent
divergence. The pin is enforced here too.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

DIALECTS = ("", "tsql", "postgres", "duckdb", "databricks")


def gostr(s: str) -> str:
    """Quote a string as Go source.

    Control characters have to be escaped, not passed through: a value with a
    literal tab in it -- and the reference has several, ByteString among them
    -- produced a Go file that would not parse. Escaping only the backslash and
    the quote was enough right up until a table carried one.
    """
    out = []
    for ch in s:
        if ch == "\\":
            out.append("\\\\")
        elif ch == '"':
            out.append('\\"')
        elif ch == "\n":
            out.append("\\n")
        elif ch == "\r":
            out.append("\\r")
        elif ch == "\t":
            out.append("\\t")
        elif ord(ch) < 0x20 or ord(ch) == 0x7F:
            out.append(f"\\x{ord(ch):02x}")
        else:
            out.append(ch)
    return '"' + "".join(out) + '"'


def ttset(name: str, tokens) -> str:
    body = "".join(f"\t\t\tTok{t.name}: {{}},\n" for t in sorted(tokens, key=lambda t: t.value))
    return f"\t\t{name}: map[TokenType]struct{{}}{{\n{body}\t\t}},\n"


def opmap(name: str, table) -> str:
    if not table:
        return f"\t\t{name}: nil,\n"
    body = "".join(
        f"\t\t\tTok{t.name}: {gostr(cls.__name__)},\n"
        for t, cls in sorted(table.items(), key=lambda kv: kv[0].value)
    )
    return f"\t\t{name}: map[TokenType]string{{\n{body}\t\t}},\n"


def strset(name: str, names) -> str:
    body = "".join(f"\t\t\t{gostr(n)}: {{}},\n" for n in sorted(names))
    return f"\t\t{name}: map[string]struct{{}}{{\n{body}\t\t}},\n"


def probe_substitutions(exp):
    """Argument kinds a builder might branch on, beyond a plain column.

    Each is a shape a caller can actually write, and each is one the reference
    is known to inspect: a cast (DuckDB reads a DATE cast differently), a
    nested call (LOWER over HEX becomes LowerHex), a string literal
    (REGEXP_REPLACE reads a trailing string as flags), a subquery (which has no
    name to wrap), and a number.
    """
    return [
        exp.cast(exp.column("__sub"), "DATE"),
        exp.cast(exp.column("__sub"), "DOUBLE"),
        exp.Hex(this=exp.column("__sub")),
        exp.Literal.string("__sub"),
        # A string a builder might READ rather than carry: PostgreSQL's
        # TO_TIMESTAMP translates 'YYYY-MM-DD' into '%Y-%m-%d', and
        # JSON_EXTRACT_PATH turns its string arguments into a JSON path. A
        # nonsense string passes through both untouched, so probing with one
        # said the spec was stable when it was not.
        exp.Literal.string("YYYY-MM-DD"),
        exp.Literal.string("$.a"),
        exp.Literal.number(1),
        exp.Subquery(this=exp.select("1")),
    ]


def gofmt(*paths):
    """Format what was just generated, here rather than in the caller.

    `make oracle` ran gofmt after each generator, so running a generator
    directly produced a file that was never formatted -- and golangci-lint does
    not check formatting, so nothing local complained. CI regenerates, formats,
    and diffs, so it failed there instead. A step you have to remember is a step
    that gets skipped; this one belongs to generation.
    """
    import shutil
    import subprocess

    if shutil.which("gofmt") is None:
        raise SystemExit("gofmt is not on PATH; the generated tables must be formatted")
    subprocess.run(["gofmt", "-w", *[str(p) for p in paths]], check=True)


def unit_aliases(builder) -> dict:
    """The unit spellings a builder normalises, read off its own closure.

    T-SQL writes DATEADD(qq, ...) and the reference records the unit as
    QUARTER, not QQ -- there is a 15-entry map behind it. Upper-casing the
    word the caller wrote gives a different tree, and there is no way to see
    that from the outside, so the map is read from the builder rather than
    guessed at or transcribed.
    """
    for cell in getattr(builder, "__closure__", None) or ():
        try:
            value = cell.cell_contents
        except ValueError:  # noqa: PERF203 -- an empty cell
            continue
        if (
            isinstance(value, dict)
            and value
            and all(isinstance(k, str) and isinstance(v, str) for k, v in value.items())
        ):
            return {k.upper(): v.upper() for k, v in value.items()}
    return {}


def call_builder(builder, args, dialect):
    """Run one of sqlglot's function builders.

    Some builders take `(args, dialect)` rather than `(args)` -- 36 of the 772
    names across the five dialects the executor configures, including ARRAY,
    ARRAY_AGG and CONCAT_WS. The probe used to call `builder(args)` only, so
    every one of them raised, yielded no spec, and the port REFUSED the name
    outright: ordinary SQL, unread, because the probe called the builder wrong.
    """
    from sqlglot.dialects.dialect import Dialect

    try:
        return builder(list(args))
    except TypeError:
        return builder(list(args), Dialect.get_or_raise(dialect or None))


def probe_functions(P, exp, dialect=""):
    """Work out what each function name builds, by asking the reference.

    A function name does not simply become a node of the same shape: COUNT
    becomes a Count whose first argument is `this`, whose remaining arguments
    collect into `expressions`, and which is always flagged `big_int`. About
    fifty names have a builder written by hand like that, and transcribing
    fifty lambdas is fifty chances to be subtly wrong.

    So the builders are run instead, with placeholder arguments, and the node
    they return is read back: each argument key is either one of the
    placeholders (a positional argument), a list of them (a variadic tail), or
    a constant the builder always sets. A name whose builder does anything
    else -- wraps its arguments, inspects their contents, parses its own
    syntax -- yields no spec and is refused by the port rather than guessed at.

    The spec derived from three arguments is then checked against one and two,
    so a builder that changes shape with its argument count is rejected too.
    """

    def placeholders(n):
        return [exp.column(f"__probe_{i}") for i in range(n)]

    def describe(node, args):
        """Map each of the node's args to a placeholder position or a constant."""
        by_id = {id(a): i for i, a in enumerate(args)}
        out = []
        for key, value in node.args.items():
            if id(value) in by_id:
                out.append((key, {"index": by_id[id(value)]}))
            elif isinstance(value, list):
                indexes = [by_id.get(id(v)) for v in value]
                if not value:
                    # An empty tail is ambiguous from this probe alone; the
                    # verification pass below settles it.
                    out.append((key, {"varlen": len(args)}))
                elif all(i is not None for i in indexes) and indexes == list(
                    range(indexes[0], indexes[0] + len(indexes))
                ):
                    out.append((key, {"varlen": indexes[0]}))
                else:
                    return None
            elif value is None or isinstance(value, (bool, str, int)):
                out.append((key, {"const": value}))
            elif isinstance(value, exp.Expr):
                # A node built FROM an argument rather than holding it:
                # DATEADD sets unit=Var(args[0].name.upper()). The argument
                # itself is nowhere in the tree -- only its NAME is -- so the
                # placeholder test above misses it and the whole builder used
                # to be rejected. 28 names in the catalogue do exactly this.
                inner = value.args.get("this")
                # The wrapper may carry more than `this`: a Literal always has
                # is_string as well, which is why the neutral DATE_TRUNC --
                # whose unit is a STRING literal rather than a Var -- was
                # rejected outright by a check for exactly one argument. The
                # extras are recorded so they can be rebuilt.
                extras = [
                    (k, v)
                    for k, v in value.args.items()
                    if k != "this" and (v is None or isinstance(v, (bool, str, int)))
                ]
                if not isinstance(inner, str) or len(extras) != len(value.args) - 1:
                    return None
                for i, a in enumerate(args):
                    if inner == a.name.upper():
                        out.append(
                            (key, {"wrap": type(value).__name__, "index": i, "extras": extras})
                        )
                        break
                else:
                    return None
            else:
                return None
        return out

    def rebuild(spec, args):
        out = {}
        for key, how in spec:
            if "wrap" in how:
                i = how["index"]
                out[key] = (
                    getattr(exp, how["wrap"])(
                        this=args[i].name.upper(), **dict(how.get("extras") or [])
                    )
                    if i < len(args)
                    else None
                )
            elif "index" in how:
                out[key] = args[how["index"]] if how["index"] < len(args) else None
            elif "varlen" in how:
                out[key] = args[how["varlen"] :]
            else:
                out[key] = how["const"]
        return out

    def same(built, node, args):
        # A key set to None is indistinguishable from an absent one: the
        # reference's zip simply stops, and neither form dumps anything. So
        # compare only the keys that actually carry a value.
        def carried(d):
            return [(k, v) for k, v in d.items() if v is not None and v != []]

        if [k for k, _ in carried(built)] != [k for k, _ in carried(node.args)]:
            return False
        for key, value in carried(built):
            actual = node.args[key]
            if isinstance(value, list) or isinstance(actual, list):
                if not isinstance(value, list) or not isinstance(actual, list):
                    return False
                if [id(v) for v in value] != [id(v) for v in actual]:
                    return False
            elif value is None or isinstance(value, (bool, str, int)):
                if actual is not value and actual != value:
                    return False
            elif isinstance(value, exp.Expr) and isinstance(actual, exp.Expr):
                # A wrapped node is rebuilt, not carried, so identity is the
                # wrong test -- compare what it is instead.
                if type(value) is not type(actual) or value.args != actual.args:
                    return False
            elif value is not actual:
                return False
        return True

    def wrapped_indexes(spec):
        """Positions whose NAME the builder takes, not the argument itself.

        A unit slot -- the DAY in DATE_DIFF(a, b, DAY) -- holds a bare word,
        and substituting a cast or a subquery there tests a call nobody can
        write. The reference keeps the cast instead of naming it, so the spec
        does not hold, and rejecting the whole arity over that turned away
        every real DATE_DIFF there is. The substitutions skip these positions,
        and the port refuses at parse time when the argument has no name.
        """
        return {how["index"] for _, how in spec if "wrap" in how}

    out = {}
    by_arity: dict[str, dict[int, tuple]] = {}
    unit_maps: dict[str, dict] = {}
    for name, builder in P.FUNCTIONS.items():
        if name in P.FUNCTION_PARSERS:
            continue
        # Probe with as many arguments as the builder will take: a key beyond
        # the widest probe would be invisible, and the function would be built
        # missing it. Twelve covers the widest signature in the reference.
        node = None
        args: list = []
        for width in range(12, -1, -1):
            args = placeholders(width)
            try:
                node = call_builder(builder, args, dialect)
            except Exception:  # noqa: BLE001 -- too many arguments for this name; try fewer
                node = None
                continue
            break
        if node is None:
            continue
        if not isinstance(node, exp.Expr):
            continue
        spec = describe(node, args)
        if spec is None:
            continue
        # The same spec must explain a call whose arguments are not all plain
        # columns. A builder that inspects what it was handed -- DuckDB's
        # DATE_TRUNC builds a DateTrunc for a DATE cast and a TimestampTrunc
        # otherwise, LOWER builds a LowerHex over a Hex -- yields a spec that
        # is right for placeholders and quietly WRONG for real SQL. Probing
        # with one kind of argument cannot see that, so the spec is put to
        # several kinds and kept only if it survives all of them.
        ok = True
        skip = wrapped_indexes(spec)
        for kind in probe_substitutions(exp):
            for i in range(len(args)):
                if i in skip:
                    continue
                subst = list(args)
                subst[i] = kind.copy()
                try:
                    other = call_builder(builder, subst, dialect)
                except Exception:  # noqa: BLE001 -- this argument is invalid here
                    continue
                if not isinstance(other, exp.Expr) or other.__class__ is not node.__class__:
                    ok = False
                    break
                if not same(rebuild(spec, subst), other, subst):
                    ok = False
                    break
            if not ok:
                break
        # And once with EVERY argument a string at the same time. PostgreSQL's
        # JSON_EXTRACT_PATH only builds a JSON path when all of its arguments
        # are strings; with placeholder columns, or with one string among
        # columns, it hands them back unchanged and looks perfectly ordinary.
        if ok and args:
            allstr = [
                exp.column(f"w{i}") if i in skip else exp.Literal.string(f"s{i}")
                for i in range(len(args))
            ]
            try:
                other = call_builder(builder, allstr, dialect)
            except Exception:  # noqa: BLE001 -- strings may be invalid for this name
                other = None
            if other is not None and isinstance(other, exp.Expr):
                if other.__class__ is not node.__class__ or not same(
                    rebuild(spec, allstr), other, allstr
                ):
                    ok = False
        for n in range(len(args)) if ok else ():
            fewer = placeholders(n)
            try:
                other = call_builder(builder, fewer, dialect)
            except Exception:  # noqa: BLE001 -- fewer arguments may be invalid for this name
                continue
            if not isinstance(other, exp.Expr) or other.__class__ is not node.__class__:
                ok = False
                break
            if not same(rebuild(spec, fewer), other, fewer):
                ok = False
                break
        if ok:
            out[name] = (node.__class__.__name__, spec)
            aliases = unit_aliases(builder)
            if aliases:
                unit_maps[name] = aliases
            continue

        # The single spec did not hold. Very often that is because the builder
        # simply MEANS something different at a different argument count --
        # DATEDIFF of two arguments and of three are not the same shape -- and
        # each count on its own is perfectly describable. 271 of the 284
        # rejected names across the five dialects are this, so they are probed
        # again one arity at a time and kept per arity.
        for width in range(13):
            narrow = placeholders(width)
            try:
                one = call_builder(builder, narrow, dialect)
            except Exception:  # noqa: BLE001 -- invalid arity for this name
                continue
            if not isinstance(one, exp.Expr):
                continue
            narrow_spec = describe(one, narrow)
            if narrow_spec is None:
                continue
            # Held to the same substitutions as any other spec: a builder that
            # inspects its arguments is refused at every arity.
            fine = True
            narrow_skip = wrapped_indexes(narrow_spec)
            for kind in probe_substitutions(exp):
                for i in range(width):
                    if i in narrow_skip:
                        continue
                    subst = list(narrow)
                    subst[i] = kind.copy()
                    try:
                        other = call_builder(builder, subst, dialect)
                    except Exception:  # noqa: BLE001
                        continue
                    if not isinstance(other, exp.Expr) or other.__class__ is not one.__class__:
                        fine = False
                        break
                    if not same(rebuild(narrow_spec, subst), other, subst):
                        fine = False
                        break
                if not fine:
                    break
            if fine and width:
                allstr = [
                    exp.column(f"w{i}") if i in narrow_skip else exp.Literal.string(f"s{i}")
                    for i in range(width)
                ]
                try:
                    other = call_builder(builder, allstr, dialect)
                except Exception:  # noqa: BLE001
                    other = None
                if other is not None and isinstance(other, exp.Expr):
                    if other.__class__ is not one.__class__ or not same(
                        rebuild(narrow_spec, allstr), other, allstr
                    ):
                        fine = False
            if fine:
                by_arity.setdefault(name, {})[width] = (one.__class__.__name__, narrow_spec)
                aliases = unit_aliases(builder)
                if aliases:
                    unit_maps[name] = aliases
    return out, by_arity, unit_maps


def reserved_keywords(dialect: str, Dialect) -> list[str]:
    """Words this dialect must QUOTE even when the caller did not.

    DuckDB reserves `all`, so `SELECT 1 AS all` is written `AS "all"`. Leaving
    it bare is a syntax error on the engine, from a statement the reference
    round-trips.
    """
    g = Dialect.get_or_raise(dialect or None).generator_class
    return sorted(str(w).upper() for w in (getattr(g, "RESERVED_KEYWORDS", None) or ()))


def array_delimiters(dialect: str, exp) -> tuple[str, str]:
    """How this dialect writes an array literal, as the text around its items.

    DuckDB writes `[1, 2]`, PostgreSQL `ARRAY[1, 2]`, and everyone else
    `ARRAY(1, 2)`. Read off the reference rather than transcribed, so a dialect
    that spells it some fourth way needs no change here.
    """
    node = exp.Array(expressions=[exp.column("__probe_0"), exp.column("__probe_1")])
    text = node.sql(dialect=dialect or None)
    head, _, rest = text.partition("__probe_0")
    _, _, tail = rest.partition("__probe_1")
    return head, tail


def interval_unit_inside_string(dialect: str, exp) -> bool:
    """Whether the unit is written INSIDE the quantity string.

    PostgreSQL writes `INTERVAL '1 DAY'`; everyone else writes
    `INTERVAL '1' DAY`. Same tree, two spellings, and sending the wrong one is
    sending the engine a different statement than the Python executor sends.
    PROBED.
    """
    node = exp.Interval(this=exp.Literal.string("1"), unit=exp.Var(this="DAY"))
    return "'1 DAY'" in node.sql(dialect=dialect or None)


def json_arrow_flags(dialect: str) -> tuple[bool, bool]:
    """What `->` and `->>` stamp on the node they build.

    PostgreSQL sets only_json_types. And PostgreSQL alone leaves scalar_only
    OFF the node, where the others set it to False -- so the second value here
    says whether the arg is PRESENT, not what it is.
    """
    from sqlglot.dialects.dialect import Dialect

    import sqlglot

    d = Dialect.get_or_raise(dialect or None)
    # scalar_only is read off the TREE, not off the flag. Every dialect has the
    # flag and every one of them has it False, yet PostgreSQL leaves the arg
    # off the node entirely while the others set it to False -- and an arg
    # present-but-false is a different tree from an arg absent. The flag could
    # not have told us that; the node could.
    node = sqlglot.parse_one("SELECT x ->> 'y'", dialect=dialect or None).selects[0]
    return (
        bool(getattr(d.parser_class, "JSON_ARROWS_REQUIRE_JSON_TYPE", False)),
        "scalar_only" in node.args,
    )


def json_path_is_parsed(dialect: str) -> bool:
    """Whether the string after `->` is PARSED as a path or kept whole.

    PostgreSQL keeps it: `j -> '$.a.b'` is one key whose name is the entire
    string. Everyone else splits it into root, key, key. Same operator, two
    trees, so it is probed.
    """
    import sqlglot

    e = sqlglot.parse_one("SELECT j -> '$.a.b'", dialect=dialect or None).selects[0]
    return len(e.args["expression"].args["expressions"]) > 2


def table_functions(dialect: str, P) -> list[str]:
    """Names that are NOT a table when they appear in a FROM clause.

    `FROM UNNEST(x)` is an Unnest in the reference, not a Table wrapping a
    call, and the port built the second -- SQL that round-trips perfectly and
    is a different tree. Nothing about the call itself says so, so every name
    in the catalogue is put in a FROM clause and the result looked at.
    """
    import sqlglot

    out = []
    for name in P.FUNCTIONS:
        if not name.replace("_", "").isalnum():
            continue
        try:
            parsed = sqlglot.parse_one(f"SELECT a FROM {name}(x)", dialect=dialect or None)
        except Exception:  # noqa: BLE001 -- not usable in a FROM clause
            continue
        source = (parsed.args.get("from_") or parsed).this
        if source is not None and type(source).__name__ != "Table":
            out.append(name)
    return sorted(out)


def writes_into_unlogged(dialect: str, exp) -> bool:
    """Whether `SELECT ... INTO UNLOGGED foo` keeps the UNLOGGED.

    PostgreSQL writes it; T-SQL has no such thing and drops it. Same tree,
    two spellings. PROBED.
    """
    node = exp.Into(this=exp.table_("foo"), temporary=False, unlogged=True)
    return "UNLOGGED" in node.sql(dialect=dialect or None)


def time_format_args(dialect: str, P, exp, funcs) -> dict:
    """Argument positions that hold a TIME FORMAT, and so are translated.

    `STRPTIME(x, '%Y-%m-%d')` in DuckDB and `FORMAT(d, 'yyyy')` in T-SQL both
    reach a builder that rewrites the format string through the dialect's
    TIME_MAPPING before storing it. The probe could not describe that -- the
    builder returns a NEW literal even where the mapping is empty and the text
    unchanged -- so every one of these names was refused.

    Flagged two ways, because the evidence differs: where the dialect has a
    mapping, substitute one of its keys and see the value come back
    translated; where it has none, translation is the identity, so a literal
    rebuilt with the same text is the signal instead.
    """
    from sqlglot.dialects.dialect import Dialect

    mapping = getattr(Dialect.get_or_raise(dialect or None), "TIME_MAPPING", None) or {}
    probe_key = next(iter(sorted(mapping)), "%Y")
    want = mapping.get(probe_key, probe_key)

    out: dict[str, list[int]] = {}
    for name in funcs:
        builder = P.FUNCTIONS.get(name)
        if builder is None:
            continue
        for width in range(1, 5):
            plain = [exp.column(f"__probeArg{chr(65 + i)}") for i in range(width)]
            try:
                base = call_builder(builder, plain, dialect)
            except Exception:  # noqa: BLE001 -- invalid arity
                continue
            if not isinstance(base, exp.Expr):
                continue
            for i in range(width):
                literal = exp.Literal.string(probe_key)
                swapped = list(plain)
                swapped[i] = literal
                try:
                    got = call_builder(builder, swapped, dialect)
                except Exception:  # noqa: BLE001
                    continue
                if not isinstance(got, exp.Expr):
                    continue
                for value in got.args.values():
                    if not isinstance(value, exp.Literal) or value is literal:
                        continue
                    if value.name == want and (mapping or value.name == probe_key):
                        out.setdefault(name, [])
                        if i not in out[name]:
                            out[name].append(i)
                        break
    return {k: sorted(v) for k, v in out.items()}


def json_extract_sql(dialect: str, exp) -> dict:
    """How `->` and `->>` are WRITTEN, when they are written as an operator.

    DuckDB writes `j -> '$.a'` and Databricks `j:a`. PostgreSQL writes
    `JSON_EXTRACT_PATH(j, 'a', 'b')` -- the path EXPLODED into one argument per
    part -- and T-SQL duplicates the whole expression into
    `ISNULL(JSON_QUERY(...), JSON_VALUE(...))`. Neither of those is the path
    rendered in place, so the substitution below simply fails for them and they
    stay refused, which is the right answer rather than a special case.
    """
    out = {}
    path = exp.JSONPath(
        expressions=[exp.JSONPathRoot(), exp.JSONPathKey(this="KEYA"), exp.JSONPathKey(this="KEYB")]
    )
    try:
        rendered_path = path.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return out
    for cls_name in ("JSONExtract", "JSONExtractScalar"):
        cls = getattr(exp, cls_name)
        node = cls(this=exp.column("THISCOL"), expression=path.copy(), only_json_types=False)
        try:
            text = node.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        if text.count("THISCOL") != 1 or rendered_path not in text:
            continue
        out[cls_name] = text.replace("THISCOL", "{this}").replace(rendered_path, "{path}")
    return out


def json_path_pieces(dialect: str, exp) -> dict:
    """How this dialect writes a JSON path, taken apart into its pieces.

    A path renders standalone and brings its own quoting, so the pieces can be
    recovered by rendering canonical paths and subtracting: DuckDB writes
    `'$.a[0]."b c"'` and Databricks `a[0]["b c"]` -- different root, different
    quoting, different way of spelling a key that needs quotes. Nothing here is
    transcribed; each piece is what the reference emitted minus what the
    shorter path emitted.
    """
    def render(parts):
        return exp.JSONPath(expressions=parts).sql(dialect=dialect or None)

    root = [exp.JSONPathRoot()]
    try:
        only_root = render(root)
        with_key = render(root + [exp.JSONPathKey(this="KEY")])
        with_sub = render(root + [exp.JSONPathSubscript(this=7)])
        with_quoted = render(root + [exp.JSONPathKey(this="a b")])
    except Exception:  # noqa: BLE001 -- this dialect will not render a bare path
        return {}

    # The outer wrapper is whatever surrounds every rendering.
    open_, close_ = "", ""
    if only_root.startswith("'") and only_root.endswith("'"):
        open_, close_ = "'", "'"
        only_root = only_root[1:-1]
        with_key = with_key[1:-1]
        with_sub = with_sub[1:-1]
        with_quoted = with_quoted[1:-1]

    def suffix(longer):
        return longer[len(only_root):] if longer.startswith(only_root) else longer

    return {
        "Open": open_ + only_root,
        "Close": close_,
        "Key": suffix(with_key).replace("KEY", "{key}"),
        "Subscript": suffix(with_sub).replace("7", "{index}"),
        "QuotedKey": suffix(with_quoted).replace("a b", "{key}"),
    }


def bracket_is_rewritten(dialect: str) -> bool:
    """Whether `a[1]` comes back as something other than a plain subscript.

    DuckDB and PostgreSQL rewrite it: the index is shifted to sqlglot's 0-based
    Bracket, the base and the index are annotated with a type, and the shift
    goes through the SIMPLIFIER -- `a[1 + 1]` arrives as `Literal(1)` and
    `a[-1]` as `Neg(2)`. All of that is the optimizer, which is not ported.
    Databricks and the neutral dialect leave the subscript alone.

    So this is not a flag to emulate but a flag to REFUSE on: where the
    reference rewrites, the port cannot follow, and a Bracket built plainly
    would be a tree the reference never produces. PROBED.
    """
    import sqlglot

    e = sqlglot.parse_one("SELECT a[1]", dialect=dialect or None).selects[0]
    if type(e).__name__ != "Bracket":
        # T-SQL opens a quoted identifier with `[`, so the subscript grammar is
        # unreachable there and the flag is moot either way.
        return False
    index = (e.args.get("expressions") or [None])[0]
    if index is None:
        return True
    return e.args["this"].type is not None or index.this != "1"


def quantifier_sql(dialect: str, exp) -> dict:
    """The text before a quantifier's operand.

    ALL is written with a trailing space and ANY without -- `ALL (x)` and
    `ANY(x)` -- and it is a property of the CLASS, uniform across dialects.
    Read off rather than assumed: comparing an ALL against an ANY suggests a
    rule about the operator above them, and there is none.
    """
    out = {}
    operand = exp.Paren(this=exp.column("ZZOPERANDZZ"))
    for cls_name in ("All", "Any"):
        node = getattr(exp, cls_name)(this=operand.copy())
        try:
            text = node.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        rendered = operand.sql(dialect=dialect or None)
        if not text.endswith(rendered):
            continue
        out[cls_name] = text[: -len(rendered)]
    return out


def default_nulls_first(dialect: str) -> tuple[bool, bool]:
    """Where NULLs sort by default, ascending and descending.

    Read off the reference by PARSING a statement that does not say, rather
    than reimplementing the rule in the generator from the same table the
    parser uses -- writing that rule twice got Databricks wrong, and the
    reference answers it directly.
    """
    import sqlglot

    def one(direction):
        o = sqlglot.parse_one(
            f"SELECT a FROM t ORDER BY a {direction}".strip(), dialect=dialect or None
        )
        return bool(o.args["order"].args["expressions"][0].args.get("nulls_first"))

    return one(""), one("DESC")


def writes_nulls_ordering(dialect: str) -> bool:
    """Whether the dialect writes NULLS FIRST/LAST back at all.

    T-SQL has no such clause and drops it; DuckDB and PostgreSQL write it when
    it differs from their own default for that direction. PROBED, because the
    difference is between silently losing an ordering and stating it.
    """
    import sqlglot

    # Probe with the OPPOSITE of this dialect's own default, or the reference
    # rightly omits a clause that says nothing and the answer looks like "does
    # not write it". Databricks sorts NULLs first ascending, so asking it about
    # NULLS FIRST asked nothing.
    asc_default, _ = default_nulls_first(dialect)
    word = "LAST" if asc_default else "FIRST"
    out = sqlglot.parse_one(f"SELECT a FROM t ORDER BY a NULLS {word}").sql(
        dialect=dialect or None
    )
    return f"NULLS {word}" in out


def boolean_sql(dialect: str, exp) -> dict:
    """How TRUE and FALSE are written, in a VALUE position and in a CONDITION.

    T-SQL has no boolean literal and the two positions differ: `SELECT 1` where
    a value is wanted, `WHERE (1 = 1)` where a condition is. Everywhere else
    both are the word itself. Read off the reference in both positions rather
    than assumed, because the difference is exactly what a port would miss.
    """
    import sqlglot

    def cond(word):
        rendered = sqlglot.parse_one(f"SELECT a FROM t WHERE {word}").sql(dialect=dialect or None)
        return rendered.split("WHERE ", 1)[1]

    return {
        "TrueValue": exp.select(exp.true()).sql(dialect=dialect or None)[len("SELECT ") :],
        "FalseValue": exp.select(exp.false()).sql(dialect=dialect or None)[len("SELECT ") :],
        "TrueCondition": cond("TRUE"),
        "FalseCondition": cond("FALSE"),
    }


def writes_boolean_literal(dialect: str, exp) -> bool:
    """Whether this dialect writes TRUE and FALSE as themselves.

    T-SQL has no boolean literal: the reference rewrites `TRUE` to `1` where a
    value is wanted, to `1 = 1` where a condition is, and `a IS TRUE` to
    `a = 1`. That is a transform over the tree, not a spelling, and the port
    does not have it -- so it must not write `TRUE` to an engine that would
    reject it. PROBED rather than assumed: ask the reference what it writes.
    """
    return exp.true().sql(dialect=dialect or None) == "TRUE" and (
        exp.false().sql(dialect=dialect or None) == "FALSE"
    )


def string_sensitive_args(P, exp, funcs, dialect=""):
    """Argument positions where a STRING LITERAL makes the builder do something
    else entirely.

    probe_functions runs each builder with placeholder COLUMNS, so it can only
    see what a builder does structurally. Some builders read their arguments
    instead: PostgreSQL's GENERATE_SERIES turns a string step into an Interval,
    and REGEXP_REPLACE routes its LAST argument to `modifiers` and drops it out
    of the positional run. Given placeholders neither rule fires, so the
    recorded spec is right for columns and quietly WRONG for strings -- it
    built a plausible tree with the argument in the wrong slot.

    Every arity is probed, not just the widest the builder accepts: "the last
    argument" is a different index in a four-argument call than in a twelve-
    argument one, and probing one arity left every real call mis-built.

    A content-blind builder puts the literal in exactly the slot the
    placeholder occupied. Any other difference -- a wrapper node, a different
    key, a shifted tail -- means the builder inspects contents, and the port
    refuses that call rather than guessing which slot was meant.
    """

    def shape(node, args):
        by_id = {id(a): i for i, a in enumerate(args)}
        out = {}
        for key, value in node.args.items():
            if value is None or value == []:
                continue
            if id(value) in by_id:
                # The TYPE counts, not just the position. PostgreSQL's
                # REGEXP_REPLACE runs sqlglot's type ANNOTATOR over its last
                # argument to decide whether it is flags or a number, and
                # stamps the result on the node. The annotator is the
                # optimizer, which this port does not have -- so a call that
                # depends on it is one the port cannot reproduce.
                out[key] = ("arg", by_id[id(value)], str(getattr(value, "type", None)))
            elif isinstance(value, list):
                out[key] = ("list", tuple(by_id.get(id(v), type(v).__name__) for v in value))
            elif isinstance(value, exp.Expr):
                out[key] = ("node", type(value).__name__, str(getattr(value, "type", None)))
            else:
                out[key] = ("const", value)
        return out

    def build(builder, args):
        try:
            node = call_builder(builder, args, dialect)
        except Exception:  # noqa: BLE001 -- this arity or argument is invalid for this name
            return None
        return node if isinstance(node, exp.Expr) else None

    sensitive: dict[str, list[int]] = {}
    for name in funcs:
        builder = P.FUNCTIONS.get(name)
        if builder is None:
            continue
        for width in range(13):
            plain = [exp.column(f"__probe_{i}") for i in range(width)]
            base = build(builder, plain)
            if base is None:
                continue
            base_shape = shape(base, plain)
            for i in range(width):
                swapped = list(plain)
                swapped[i] = exp.Literal.string("__probe_str")
                other = build(builder, swapped)
                if other is None or type(other) is not type(base):
                    sensitive.setdefault(name, []).append(i)
                elif shape(other, swapped) != base_shape:
                    sensitive.setdefault(name, []).append(i)
    return {k: sorted(set(v)) for k, v in sensitive.items()}


def _record(sensitive, name, arity, index, keyof):
    """Record a sensitive position by the node ARG KEY it lands on.

    The call's argument order and the node's key order are not the same thing:
    DATE_DIFF(unit, a, b) puts its unit FIRST in the call and stores it last in
    the node, so an index recorded from the call meant nothing to a generator
    reading keys. The key is the same on both sides.
    """
    key = (keyof or {}).get(index)
    if key is None:
        return
    bucket = sensitive.setdefault(name, {}).setdefault(arity, [])
    if key not in bucket:
        bucket.append(key)


def _cast_probe(builder, plain, base_sql, i, probe, dialect, name, sensitive, keyof=None):
    """One substitution for cast_sensitive_args, recorded per ARITY.

    DuckDB's ROUND wraps its second argument in CAST(... AS INT) when the call
    has four arguments and not when it has two. Recording the index alone
    refused `ROUND(x, 0)` -- an ordinary two-argument call that renders
    perfectly -- because something was true of a four-argument one.
    """
    try:
        probe_sql = probe.sql(dialect=dialect or None)
        swapped = list(plain)
        swapped[i] = probe
        got = call_builder(builder, swapped, dialect).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001 -- this argument is invalid here
        _record(sensitive, name, len(plain), i, keyof)
        return
    token = f"__probeArg{chr(65 + i)}"
    if base_sql.count(token) != 1:
        return
    if probe_sql not in got:
        # The argument VANISHED: DuckDB drops a zero group from
        # REGEXP_EXTRACT entirely. Writing it back is a different call, and
        # its absence is the signal rather than something to skip over.
        _record(sensitive, name, len(plain), i, keyof)
        return
    if got.count(probe_sql) != 1:
        return
    if got != base_sql.replace(token, probe_sql, 1):
        _record(sensitive, name, len(plain), i, keyof)


def cast_sensitive_args(P, exp, dialect, funcs):
    """Argument positions where an explicitly CAST argument changes the call's
    own rendering.

    DuckDB's BIT_OR over a non-integer becomes
    `BIT_OR(CAST(ROUND(CAST(x AS REAL)) AS INT))`, and PostgreSQL's two-argument
    ROUND over a double gains a CAST to DECIMAL. Both fire only when the
    argument's type is VISIBLE -- a bare column is left alone, because the
    reference cannot type it either -- so the trigger is an explicit cast to a
    non-integer type, which a port CAN see.

    This is a rendering rule, not a parse rule: the tree is identical either
    way, and only the SQL differs. So it is probed by rendering. A call whose
    scaffolding changes when an argument is cast is refused, because the port
    would otherwise hand the engine a different statement than the Python
    executor does.
    """
    probe = exp.column("__probe_0")
    sensitive: dict[str, dict[int, list[str]]] = {}
    zero_sensitive: dict[str, dict[int, list[str]]] = {}
    for name in funcs:
        builder = P.FUNCTIONS.get(name)
        if builder is None:
            continue
        for width in range(1, 6):
            # Letters, not digits: with `__probe_0` the text "0" is a substring
            # of another argument's name, and the test for an argument that
            # VANISHED could never fire.
            plain = [exp.column(f"__probeArg{chr(65 + i)}") for i in range(width)]
            try:
                base = call_builder(builder, plain, dialect)
                base_sql = base.sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001 -- invalid arity or unrenderable for this name
                continue
            # Which node arg key each call position landed on. The call's
            # argument order and the node's key order are not the same thing --
            # DATE_DIFF(unit, a, b) stores its unit LAST -- so a position taken
            # from the call meant nothing to a generator reading keys.
            by_id = {id(a): n for n, a in enumerate(plain)}
            keyof: dict[int, str] = {}
            for key, value in base.args.items():
                if id(value) in by_id:
                    keyof[by_id[id(value)]] = key
                elif isinstance(value, exp.Expr):
                    for inner in value.walk():
                        if id(inner) in by_id:
                            keyof[by_id[id(inner)]] = key
                            break
            for i in range(width):
                # Two things can change how the CALL is written: an argument
                # whose type is visible (a cast), and a literal ZERO, which
                # DuckDB drops entirely from REGEXP_EXTRACT.
                # Two DIFFERENT triggers, kept apart. A call whose rendering
                # moves for a cast says nothing about what it does with a
                # number, and refusing on both because one was observed turned
                # away 25 statements that render perfectly well.
                cls_name = type(base).__name__
                _cast_probe(
                    builder, plain, base_sql, i,
                    exp.cast(exp.column(f"__probeArg{chr(65 + i)}"), "DOUBLE"),
                    dialect, cls_name, sensitive, keyof,
                )
                _cast_probe(
                    builder, plain, base_sql, i, exp.Literal.number(0),
                    dialect, cls_name, zero_sensitive, keyof,
                )
                # A STRING can be coerced too: DuckDB writes
                # DATE_DIFF('QUARTER', CAST('2009-02-13' AS DATE), ...), adding
                # a cast the tree does not carry. Same bucket as the number,
                # since both are "a literal the reference wraps".
                _cast_probe(
                    builder, plain, base_sql, i, exp.Literal.string("2009-02-13"),
                    dialect, cls_name, zero_sensitive, keyof,
                )
            continue

        for _unused in ():
            for i in range(width):
                cast = exp.cast(exp.column(f"__probe_{i}"), "DOUBLE")
                try:
                    cast_sql = cast.sql(dialect=dialect or None)
                    swapped = list(plain)
                    swapped[i] = cast
                    got = call_builder(builder, swapped, dialect).sql(dialect=dialect or None)
                except Exception:  # noqa: BLE001 -- a cast may be invalid here
                    sensitive.setdefault(name, []).append(i)
                    continue
                # Only meaningful when the argument is rendered verbatim and
                # once: a name that reorders, repeats or rewrites its arguments
                # fails this substitution for reasons that have nothing to do
                # with the cast, and flagging those would refuse most of the
                # function catalogue.
                token = f"__probe_{i}"
                if base_sql.count(token) != 1 or got.count(cast_sql) != 1:
                    continue
                if got != base_sql.replace(token, cast_sql, 1):
                    sensitive.setdefault(name, []).append(i)
    _ = probe
    def tidy(d):
        return {n: {a: sorted(set(v)) for a, v in by.items()} for n, by in d.items()}

    return tidy(sensitive), tidy(zero_sensitive)


def class_sensitive_args(P, exp, dialect, funcs):
    """Argument positions where the argument's own CLASS changes what is built.

    DuckDB's LOWER is `LowerHex(this=arg.this)` when the argument is a Hex and
    `Lower(this=arg)` otherwise -- so `LOWER(HEX(x))` is a different node from
    `LOWER(x)`, and a spec probed with a placeholder column describes only the
    second. The port built `Lower` for both, which is a tree the reference never
    makes.

    Neither of the other sensitivity probes finds it: one substitutes a string
    literal, the other a cast, and Hex is neither. So this one substitutes every
    class that ANY builder in this dialect's own catalogue produces for a
    one-argument call -- the space of nested calls a caller could actually
    write -- and flags a position whose returned class moves. The candidate set
    comes from the reference, so a builder added upstream that branches on some
    new class is found without anyone noticing it was added.
    """
    candidates: dict[str, object] = {}
    for builder in P.FUNCTIONS.values():
        try:
            node = call_builder(builder, [exp.column("__inner")], dialect)
        except Exception:  # noqa: BLE001 -- not a one-argument name
            continue
        if isinstance(node, exp.Expr):
            candidates.setdefault(type(node).__name__, node)

    out: dict[str, dict[int, list[str]]] = {}
    for name in funcs:
        builder = P.FUNCTIONS.get(name)
        if builder is None:
            continue
        for width in range(1, 4):
            plain = [exp.column(f"__probe_{i}") for i in range(width)]
            try:
                base = call_builder(builder, plain, dialect)
            except Exception:  # noqa: BLE001 -- invalid arity for this name
                continue
            if not isinstance(base, exp.Expr):
                continue
            for i in range(width):
                for cname, cnode in candidates.items():
                    swapped = list(plain)
                    swapped[i] = cnode.copy()
                    try:
                        got = call_builder(builder, swapped, dialect)
                    except Exception:  # noqa: BLE001 -- invalid argument here
                        continue
                    if isinstance(got, exp.Expr) and type(got) is not type(base):
                        out.setdefault(name, {}).setdefault(i, [])
                        if cname not in out[name][i]:
                            out[name][i].append(cname)
    return {n: {i: sorted(c) for i, c in sorted(d.items())} for n, d in out.items()}


def observed_shapes(exp, dialect, repo):
    """Which nodes the corpus actually contains, and with which arguments set.

    The template probe used to work from a hand-written list of four classes.
    Everything outside it had no spelling and was refused -- 44 statements for
    JSONExtract alone, which four dialects write four ways. The list is now the
    CORPUS: parse every statement with the reference and record each node's
    class together with the arguments it actually carries, so a template is
    probed for exactly the shapes that occur and no others.
    """
    import json

    import sqlglot

    index = repo / "testdata" / "expected" / "index.json"
    if not index.exists():
        return {}
    shapes: dict[str, set] = {}
    for entry in json.loads(index.read_text())["statements"]:
        if (entry["dialect"] or "") != dialect:
            continue
        try:
            tree = sqlglot.parse_one(entry["sql"], read=dialect or None)
        except Exception:  # noqa: BLE001 -- the reference cannot read it either
            continue
        for node in tree.walk():
            expr_keys, scalars = [], []
            for key, value in node.args.items():
                if value is None or value == []:
                    continue
                if isinstance(value, (exp.Expr, list)):
                    expr_keys.append(key)
                elif isinstance(value, str):
                    scalars.append((key, value))
            if expr_keys or scalars:
                shapes.setdefault(type(node).__name__, set()).add(
                    (tuple(expr_keys), tuple(scalars))
                )
    return shapes


# Shapes the PORT can build that the corpus happens not to contain for every
# dialect. The corpus is the primary source -- these are a floor under it, not
# a replacement, and each corresponds to a form the port's own parser produces.
EXTRA_SHAPES = {
    "Trim": [
        (("this",), ()),
        (("this", "expression"), ()),
        (("this", "expression"), (("position", "BOTH"),)),
        (("this", "expression"), (("position", "LEADING"),)),
        (("this", "expression"), (("position", "TRAILING"),)),
    ],
    "Substring": [(("this",), ()), (("this", "start"), ()), (("this", "start", "length"), ())],
    "StrPosition": [(("this", "substr"), ())],
    "Extract": [(("this", "expression"), ())],
}


def syntax_templates(exp, dialect, repo):
    """How the reference WRITES each shape the corpus contains.

    Nothing here is spelled out. A node is built with placeholder columns for
    its expression arguments and its own values for the rest, rendered by the
    reference, and the placeholders replaced by markers -- so the template is
    whatever it actually emits. That is how DuckDB writing POSITION as
    STRPOS(b, a), T-SQL as CHARINDEX(a, b) and PostgreSQL as POSITION(a IN b)
    all arrive without a line of dialect logic.
    """
    out = {}
    shapes = observed_shapes(exp, dialect, repo)
    for cls_name, extra in EXTRA_SHAPES.items():
        shapes.setdefault(cls_name, set()).update(extra)
    for cls_name, variants in sorted(shapes.items()):
        cls = getattr(exp, cls_name, None)
        if cls is None:
            continue
        for expr_keys, scalars in sorted(variants):
            # An ALREADY-UPPERCASE placeholder. A unit comes back upper-cased --
            # T-SQL renders DATEADD(__AUNIT__, ...) from a `unit` argument --
            # and a lowercase marker simply is not there to replace, so every
            # call form of DateAdd was rejected for want of a token.
            kwargs = {k: exp.column(f"ZZ{k.upper()}ZZ") for k in expr_keys}
            kwargs.update(dict(scalars))
            try:
                text = cls(**kwargs).sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001 -- this dialect will not write that shape
                continue
            ok = True
            for key in expr_keys:
                token = f"ZZ{key.upper()}ZZ"
                if text.count(token) != 1:
                    ok = False
                    break
                text = text.replace(token, "{" + key + "}")
            if not ok:
                continue
            # A scalar that does not APPEAR in the text is not a marker, it
            # is a condition: LTRIM is Trim(position='LEADING') and RTRIM is
            # Trim(position='TRAILING'), and both render without the word. The
            # template belongs to that value, so it is recorded as a
            # requirement or the first one wins for both.
            required = []
            for key, value in scalars:
                if text.count(value) == 1:
                    text = text.replace(value, "{" + key + "}")
                else:
                    required.append((key, value))
            marked = [k for k in expr_keys] + [
                k for k, _ in scalars if (k, dict(scalars)[k]) not in required
            ]
            # Infix templates are rejected. `a #> b` needs parentheses around a
            # child by PRECEDENCE, and a template substitutes text without
            # knowing any -- the reference writes `a #> (n IN (1, 2))` and a
            # template would write it flat. A template that begins with an
            # argument is infix; the classes that need one already have a
            # writer that knows the precedence table.
            if text.lstrip().startswith("{"):
                continue
            # A CAST the probe did not ask for is a COERCION the reference
            # applies by type: DuckDB writes BOOL_OR(CAST(x AS BOOLEAN)) only
            # when x is not already boolean. The probe feeds plain columns, so
            # any cast here was added, and baking it into the template wrapped
            # an argument that already had one.
            if "CAST(" in text and cls_name not in ("Cast", "TryCast"):
                continue
            # A marker inside QUOTES is kept, not rejected: the template is
            # quoting the argument itself, so what belongs there is the
            # argument's NAME rather than its rendered SQL. Substituting the
            # rendering wrote ''ISOWEEK''; the writer looks at the quote and
            # substitutes the name instead.
            keys = list(expr_keys) + [k for k, _ in scalars]
            out.setdefault(cls_name, []).append((keys, marked, required, text))
    return out


def render_functions(P, exp, dialect, funcs):
    """How the reference WRITES each function node, for the generator.

    The parser records no spelling for a named function -- a Count node does
    not remember that it was written COUNT -- so the generator has to know the
    keyword. Rather than transcribe it, render each node with placeholder
    arguments and read the keyword back off the result.

    The keyword is not a property of the class alone: in T-SQL a Count writes
    COUNT_BIG when its big_int flag is set and COUNT when it is not, and a
    Coalesce writes ISNULL when is_null is set. So each candidate carries the
    constant arguments it applies to, and the generator picks the one that
    matches the node in hand.

    Only the plain `NAME(a, b, c)` shape is recorded, plus the bare `NAME` a
    no-argument function like CURRENT_DATE writes. A function the reference
    writes some other way -- CAST(x AS y), TRIM(x FROM y), an infix operator --
    is left out, and the generator refuses it rather than emitting something
    that would parse back into a different node.
    """
    out = {}
    for name, (cls_name, spec) in funcs.items():
        cls = getattr(exp, cls_name, None)
        if cls is None:
            continue
        positional = [kv for kv in spec if "index" in kv[1] or "varlen" in kv[1]]
        keys = [k for k, _ in positional]
        args = [exp.column(f"__probe_{i}") for i in range(len(keys))]
        kwargs = {}
        for i, (key, how) in enumerate(positional):
            kwargs[key] = [args[i]] if "varlen" in how else args[i]
        consts = [(k, how["const"]) for k, how in spec if "const" in how]
        kwargs.update(dict(consts))
        try:
            rendered = cls(**kwargs).sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001 -- a node that will not render is not supported
            continue

        # Probe every argument count the call can have, widest first. MAX of
        # one argument is MAX; of two, PostgreSQL writes GREATEST. Each
        # narrower form records the keys that must be absent for it to apply.
        candidates = out.setdefault(cls_name, [])
        for width in range(len(keys), -1, -1):
            narrowed = dict(consts)
            for i, (key, how) in enumerate(positional[:width]):
                narrowed[key] = [args[i]] if "varlen" in how else args[i]
            try:
                rendered = cls(**narrowed).sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001
                continue
            used = keys[:width]
            absent = consts + [(k, None) for k in keys[width:]]
            expected = name + "(" + ", ".join(f"__probe_{i}" for i in range(width)) + ")"
            if rendered == expected:
                entry = (name, used, absent, False)
            elif width == 0 and rendered == name:
                entry = (name, [], absent, True)
            else:
                continue
            if entry not in candidates:
                candidates.append(entry)
        if not candidates:
            del out[cls_name]

    # Most constrained first, so a node with a flag set matches the spelling
    # that flag selects rather than the general one.
    for candidates in out.values():
        candidates.sort(key=lambda c: -len(c[2]))
    return out


def sqlmap(name: str, rendered) -> str:
    lines = []
    for cls in sorted(rendered):
        entries = []
        for keyword, keys, consts, no_parens in rendered[cls]:
            arg_keys = ", ".join(gostr(k) for k in keys)
            const_parts = ", ".join(f"{{{gostr(k)}, {goconst(v)}}}" for k, v in consts)
            entries.append(
                f"{{{gostr(keyword)}, []string{{{arg_keys}}}, "
                f"[]FuncConst{{{const_parts}}}, {str(no_parens).lower()}}}"
            )
        lines.append(f"\t\t\t{gostr(cls)}: {{{', '.join(entries)}}},\n")
    return f"\t\t{name}: map[string][]FuncSQL{{\n{''.join(lines)}\t\t}},\n"


def render_operators(P, exp, Dialect, dialect):
    """The infix and prefix spelling of every operator node, per dialect.

    Same probe as the functions: build the node with placeholders, render it,
    and read the operator back out. DuckDB writes Pow as `a ** b` where the
    default writes `POWER(a, b)`, and neither is derivable from the parse
    table -- so both are read from the generator rather than assumed.
    """
    binary, unary = {}, {}
    tables = (
        P.DISJUNCTION, P.CONJUNCTION, P.EQUALITY, P.COMPARISON,
        P.BITWISE, P.TERM, P.FACTOR, P.EXPONENT, P.RANGE_PARSERS,
    )
    classes = set()
    for table in tables:
        for value in table.values():
            if isinstance(value, type) and issubclass(value, exp.Expr):
                classes.add(value)
    classes |= {
        exp.DPipe, exp.Is, exp.Like, exp.ILike, exp.BitwiseLeftShift, exp.BitwiseRightShift,
        exp.Glob, exp.RegexpLike, exp.SimilarTo, exp.NullSafeEQ, exp.NullSafeNEQ,
        exp.Distance, exp.DistanceNd, exp.IntDiv, exp.Pow, exp.Collate,
    }

    # Predicates rather than bare columns: T-SQL coerces a column used as a
    # boolean into `x <> 0`, which would be read back as part of the operator.
    def operand(n):
        return exp.EQ(this=exp.column(f"__probe_{n}"), expression=exp.Literal.number(n))

    a, b = operand(0), operand(1)
    left = a.sql(dialect=dialect or None)
    right = b.sql(dialect=dialect or None)
    d = Dialect.get_or_raise(dialect or None)
    for cls in classes:
        extra = {}
        if cls is exp.Div:
            # A Div records how the dialect divides, and renders differently
            # without those flags -- the parser always sets them.
            extra = {"typed": d.TYPED_DIVISION, "safe": d.SAFE_DIVISION}
        try:
            rendered = cls(this=a.copy(), expression=b.copy(), **extra).sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        if rendered.startswith(left + " ") and rendered.endswith(" " + right):
            binary[cls.__name__] = rendered[len(left) + 1 : -(len(right) + 1)]

    for cls in (exp.Neg, exp.BitwiseNot, exp.Not):
        try:
            rendered = cls(this=a.copy()).sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        if rendered.endswith(left):
            unary[cls.__name__] = rendered[: -len(left)]
    return binary, unary


def strstrmap(name: str, table) -> str:
    if not table:
        return f"\t\t{name}: nil,\n"
    body = "".join(f"\t\t\t{gostr(k)}: {gostr(v)},\n" for k, v in sorted(table.items()))
    return f"\t\t{name}: map[string]string{{\n{body}\t\t}},\n"


def goconst(v) -> str:
    if v is None:
        return "nil"
    if isinstance(v, bool):
        return str(v).lower()
    if isinstance(v, int):
        return str(v)
    return gostr(v)


def funcargs(spec) -> str:
    parts = []
    for key, how in spec:
        if "wrap" in how:
            extras = "".join(
                "{%s, %s}, " % (gostr(k), goconst(v)) for k, v in (how.get("extras") or [])
            )
            parts.append(
                f"{{{gostr(key)}, {how['index']}, false, nil, {gostr(how['wrap'])}, "
                f"[]FuncConst{{{extras}}}}}"
            )
        elif "index" in how:
            parts.append(f'{{{gostr(key)}, {how["index"]}, false, nil, "", nil}}')
        elif "varlen" in how:
            parts.append(f'{{{gostr(key)}, {how["varlen"]}, true, nil, "", nil}}')
        else:
            parts.append(f'{{{gostr(key)}, -1, false, {goconst(how["const"])}, "", nil}}')
    return ", ".join(parts)


def funcmap(name: str, funcs) -> str:
    """One spec per name, sharing funcargs so the two tables cannot drift."""
    lines = []
    for fn in sorted(funcs):
        cls, spec = funcs[fn]
        lines.append(f"\t\t\t{gostr(fn)}: {{{gostr(cls)}, []FuncArg{{{funcargs(spec)}}}}},\n")
    return f"\t\t{name}: map[string]FuncSpec{{\n{''.join(lines)}\t\t}},\n"


def bare_join_is_on_true(dialect: str) -> bool:
    """Whether `JOIN u` with no ON records `ON TRUE`.

    Probed rather than read: the rule lives in a dialect's parser override,
    and the two shapes produce different SQL from the same relation.
    """
    import sqlglot

    tree = sqlglot.parse_one("SELECT a FROM t JOIN u", dialect=dialect or None)
    joins = tree.args.get("joins") or []
    return bool(joins) and joins[0].args.get("on") is not None


def default_type_params(dialect: str) -> dict[str, list[str]]:
    """Parameters a bare type gains in this dialect, recovered by parsing one.

    sqlglot applies these through TYPE_CONVERTERS, a map of closures. The
    constants inside cannot be read, so the only honest way to record them is
    to parse `CAST(x AS <TYPE>)` and see what came back.
    """
    import sqlglot
    from sqlglot.dialects.dialect import Dialect

    parser = Dialect.get_or_raise(dialect or None).parser_class
    out: dict[str, list[str]] = {}
    for kind in getattr(parser, "TYPE_CONVERTERS", {}) or {}:
        name = kind.value
        parsed = sqlglot.parse_one(f"CAST(x AS {name})", dialect=dialect or None).to
        params = [str(e.this.this) for e in parsed.args.get("expressions") or []]
        if params:
            out[name] = params
    return out


def drops_type_params(dialect: str) -> list[str]:
    """Types whose parameters this dialect DISCARDS at parse time.

    The mirror of default_type_params, through the same TYPE_CONVERTERS map.
    DuckDB reads every text type as TEXT and drops the length: `VARCHAR(5)`
    parses to a bare TEXT, so a port that kept the 5 sent the engine a
    different CAST than the Python executor sent.

    Probed the same way and for the same reason -- the converters are
    closures, so what they discard cannot be read out, only observed.
    """
    import sqlglot
    from sqlglot.dialects.dialect import Dialect

    parser = Dialect.get_or_raise(dialect or None).parser_class
    out: list[str] = []
    for kind in getattr(parser, "TYPE_CONVERTERS", {}) or {}:
        name = kind.value
        try:
            parsed = sqlglot.parse_one(f"CAST(x AS {name}(5))", dialect=dialect or None).to
        except Exception:  # noqa: BLE001 -- a type that takes no parameters simply is not one
            continue
        if not (parsed.args.get("expressions") or []):
            out.append(name)
    return sorted(out)


def limit_all_means_no_limit(dialect: str) -> bool:
    """Whether `LIMIT ALL` is this dialect's way of saying "no limit".

    PostgreSQL, DuckDB and Databricks read it that way and the reference
    records it by setting NO limit; T-SQL and the neutral dialect read ALL as
    an ordinary column called "all". Same five characters, and the difference
    is the one clause this service rewrites -- so it is asked of the
    reference, not assumed either way.
    """
    import sqlglot

    tree = sqlglot.parse_one("SELECT 1 FROM t LIMIT ALL", dialect=dialect or None)
    return tree.args.get("limit") is None


def is_not_null_wraps_in_not(dialect: str) -> bool:
    """Whether `x IS NOT NULL` comes back as a Not wrapping an Is.

    Asked of the reference rather than read out of it: there is no flag to
    read. sqlglot decides this in a dialect override, so the only honest way
    to record it is to parse the statement and look at what came back.
    """
    import sqlglot

    tree = sqlglot.parse_one("SELECT 1 FROM t WHERE x IS NOT NULL", dialect=dialect or None)
    return type(tree.args["where"].this).__name__ == "Not"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--out", default="sqlglot/parser_gen.go", type=pathlib.Path)
    a = ap.parse_args()

    repo = pathlib.Path(__file__).resolve().parent.parent
    sys.path.insert(0, str(a.sqlglot))
    sys.path.insert(0, str(repo / "harness"))
    from oracle import pinned_commit, reference_commit  # noqa: PLC0415

    actual, pinned = reference_commit(a.sqlglot), pinned_commit(repo)
    if actual != pinned:
        raise SystemExit(
            f"reference checkout is at {actual[:12]} but NOTICE pins {pinned[:12]}."
        )

    from sqlglot import expressions as exp  # noqa: PLC0415
    from sqlglot.dialects.dialect import Dialect  # noqa: PLC0415

    out = [
        "// Code generated by harness/gen_parser.py from the pinned reference. DO NOT EDIT.\n//\n",
        "// The parser's keyword and function tables, read off the reference's resolved\n",
        "// parser classes so inherited dialect overrides come across intact.\n\n",
        "package sqlglot\n\n",
        "// ParserTables is one dialect's resolved parser vocabulary.\n",
        "type ParserTables struct {\n",
        "\t// IDVarTokens are keywords that may be used as an identifier.\n",
        "\tIDVarTokens map[TokenType]struct{}\n",
        "\t// TableAliasTokens are keywords that may be used as a table alias --\n",
        "\t// a strict subset: a keyword that can start a clause is excluded, or an\n",
        "\t// implicit alias would swallow it.\n",
        "\tTableAliasTokens map[TokenType]struct{}\n",
        "\t// NamedFunctions are function names the reference parses into a node\n",
        "\t// class of their own. Everything else with an argument list is an\n",
        "\t// Anonymous call, which is the only form this port builds so far -- a\n",
        "\t// name in here is refused rather than flattened into Anonymous.\n",
        "\tNamedFunctions map[string]struct{}\n",
        "\t// NoParenFunctions are names that are a function call without an\n",
        "\t// argument list at all, e.g. CURRENT_DATE.\n",
        "\tNoParenFunctions map[TokenType]struct{}\n",
        "\t// NoParenFunctionClasses is the node each of those builds, with no\n",
        "\t// arguments at all.\n",
        "\tNoParenFunctionClasses map[TokenType]string\n",
        "\t// Functions is what each named function builds, worked out by running\n",
        "\t// the reference's own builder. A name in NamedFunctions but absent here\n",
        "\t// builds something no fixed mapping describes, and is refused rather\n",
        "\t// than approximated.\n",
        "\tFunctions map[string]FuncSpec\n",
        "\t// FunctionSQL is how the generator writes each of those nodes back\n",
        "\t// out. A node absent here is one the reference writes in some shape\n",
        "\t// other than NAME(a, b); the generator refuses it rather than\n",
        "\t// emitting something that would parse back differently.\n",
        "\tFunctionSQL map[string][]FuncSQL\n",
        "\t// BinarySQL and UnarySQL are how each operator node is written.\n",
        "\t// DuckDB writes Pow as ** where the default writes POWER(a, b), so\n",
        "\t// the spelling is read from the reference's generator, not assumed.\n",
        "\tBinarySQL map[string]string\n",
        "\tUnarySQL  map[string]string\n",
        "\t// LimitIsTop puts the row ceiling in front of the projections, as\n",
        "\t// T-SQL's TOP does, rather than after the query as LIMIT does. The\n",
        "\t// guard rewrites one node either way; the dialect decides where it\n",
        "\t// lands, which is the whole reason the rewrite works at all.\n",
        "\t// TypeSQL is how each data type is written in this dialect: T-SQL\n",
        "\t// spells DataType.Type.INT as INTEGER.\n",
        "\tTypeSQL map[string]string\n",
        "\t// IdentifierStart and IdentifierEnd are the delimiters a quoted name\n",
        "\t// is written with: brackets in T-SQL, backticks in Databricks.\n",
        "\tIdentifierStart string\n",
        "\tIdentifierEnd   string\n",
        "\t// QualifiesDerivedOutputs names every unnamed column of a CTE or a\n",
        "\t// derived table on the way out, because T-SQL will not accept one\n",
        "\t// without a name. Two executors that disagreed here would send the\n",
        "\t// engine different SQL for the same question.\n",
        "\tQualifiesDerivedOutputs bool\n",
        "\t// CoercesBooleans marks a dialect with no boolean type, where a value\n",
        "\t// used as a condition is written `x <> 0`. The port does not perform\n",
        "\t// that rewrite; it refuses, because emitting the uncoerced form would\n",
        "\t// be a statement the engine rejects.\n",
        "\tCoercesBooleans bool\n",
        "\tLimitIsTop bool\n",
        "\t// StatementTokens begin a statement that is not a query -- CREATE,\n",
        "\t// DELETE, INSERT and the rest. Anything else at the top level is\n",
        "\t// parsed as a bare expression, which is what the reference does and\n",
        "\t// what most of its own fixture corpus consists of.\n",
        "\tStatementTokens map[TokenType]struct{}\n",
        "\t// FuncTokens are the tokens allowed to name a function call.\n",
        "\tFuncTokens map[TokenType]struct{}\n",
        "\t// NoParenFunctionNames are names that are a call with no argument\n",
        "\t// list -- CURDATE in Databricks, CASE and IF everywhere -- and so are\n",
        "\t// not available as a bare column name.\n",
        "\tNoParenFunctionNames map[string]struct{}\n",
        "\t// TypedDivision and SafeDivision are recorded on every Div node; the\n",
        "\t// reference reads them off the dialect, so they are not always false.\n",
        "\tTypedDivision bool\n",
        "\tSafeDivision  bool\n",
        "\t// DPipeIsStringConcat decides whether || concatenates or is a logical\n",
        "\t// or; StrictStringConcat becomes the DPipe node's safe flag, inverted.\n",
        "\tDPipeIsStringConcat bool\n",
        "\tStrictStringConcat  bool\n",
        "\t// JoinsHaveEqualPrecedence makes a comma join an explicit CROSS join.\n",
        "\tJoinsHaveEqualPrecedence bool\n",
        "\t// BareJoinIsOnTrue: a JOIN written with no ON. Databricks records it\n",
        "\t// as `ON TRUE` explicitly; every other dialect leaves the slot empty\n",
        "\t// and writes the comma form. Same relation either way, different\n",
        "\t// tree and different SQL -- so the two executors would send the\n",
        "\t// engine different statements. PROBED, like the rest.\n",
        "\tBareJoinIsOnTrue bool\n",
        "\t// DefaultTypeParams are the parameters a BARE type gains in this\n",
        "\t// dialect. DuckDB reads `numeric` as DECIMAL(18, 3) at parse time --\n",
        "\t// sqlglot calls these TYPE_CONVERTERS -- so a port that left the type\n",
        "\t// unparameterised sent a different CAST to the engine than the Python\n",
        "\t// executor did, and on a division that is a different NUMBER.\n",
        "\t// PROBED: the converters are closures, so the constants cannot be\n",
        "\t// read out; they are recovered by parsing a bare type and looking.\n",
        "\tDefaultTypeParams map[string][]string\n",
        "\t// IsNotNullWrapsInNot says how `x IS NOT NULL` is SHAPED. Every\n",
        "\t// dialect but PostgreSQL builds Not(Is(x, NULL)) and writes it back\n",
        "\t// as `NOT x IS NULL`; PostgreSQL records the negation on the Is\n",
        "\t// node instead. PROBED, not transcribed -- sqlglot has no flag for\n",
        "\t// it, the rule lives in a dialect override, and a port that assumed\n",
        "\t// one shape diverged on one of the commonest predicates in SQL.\n",
        "\t// DropsTypeParams are types whose PARAMETERS this dialect discards.\n",
        "\t// The mirror of DefaultTypeParams, out of the same TYPE_CONVERTERS:\n",
        "\t// DuckDB reads every text type as TEXT and drops the length, so\n",
        "\t// `VARCHAR(5)` is a bare TEXT and a port that kept the 5 sent the\n",
        "\t// engine a different CAST. PROBED, for the same reason.\n",
        "\tDropsTypeParams map[string]bool\n",
        "\t// LimitAllMeansNoLimit: `LIMIT ALL` is PostgreSQL for \"no limit\",\n",
        "\t// and DuckDB and Databricks follow. T-SQL and the neutral dialect\n",
        "\t// read ALL as a column of that name instead. PROBED: the difference\n",
        "\t// lands on the one clause the guard rewrites.\n",
        "\tLimitAllMeansNoLimit bool\n",
        "\t// StringSensitiveArgs are argument positions where a STRING\n",
        "\t// LITERAL makes the reference build something structurally\n",
        "\t// different: PostgreSQL reads a string step to GENERATE_SERIES as\n",
        "\t// an Interval, and a trailing string to REGEXP_REPLACE as\n",
        "\t// `modifiers`, shifting the arguments after it. The function probe\n",
        "\t// uses placeholder COLUMNS, so neither rule fires and the recorded\n",
        "\t// signature is right for columns and wrong for strings. A call that\n",
        "\t// puts a string in one of these slots is REFUSED: the port cannot\n",
        "\t// tell which slot was meant, and a plausible tree is the one thing\n",
        "\t// it must not build. PROBED.\n",
        "\t// CastSensitiveArgs are argument positions where an explicitly\n",
        "\t// CAST argument changes how the CALL itself is written: DuckDB\n",
        "\t// wraps BIT_OR over a non-integer in a round-and-cast, PostgreSQL\n",
        "\t// casts a double before a two-argument ROUND. The tree is the same\n",
        "\t// either way -- only the SQL differs -- so this is probed by\n",
        "\t// RENDERING, and a call that would need it is refused rather than\n",
        "\t// written without the coercion the engine needs. PROBED.\n",
        "\t// ClassSensitiveArgs are argument positions where the CLASS of the\n",
        "\t// argument itself changes what the builder makes: LOWER(HEX(x)) is a\n",
        "\t// LowerHex, LOWER(x) is a Lower. A spec probed with a placeholder\n",
        "\t// column describes only the second, so the call is refused when the\n",
        "\t// argument is one of the listed classes. PROBED against every class\n",
        "\t// this catalogue can itself produce.\n",
        "\t// FunctionsByArity holds names whose shape depends on HOW MANY\n",
        "\t// arguments the call has -- DATEDIFF of two is not DATEDIFF of\n",
        "\t// three. One spec cannot describe those, so each count gets its\n",
        "\t// own, and a count with no entry is refused.\n",
        "\t// UnitAliases are the unit spellings a name normalises: T-SQL\n",
        "\t// records DATEADD(qq, ...) as QUARTER, not QQ. Read off the\n",
        "\t// builder itself, since nothing outside it shows the mapping.\n",
        "\tUnitAliases map[string]map[string]string\n",
        "\t// SyntaxFunctions parse their arguments with their own grammar\n",
        "\t// rather than as a comma-separated list. A name here that the\n",
        "\t// port has not implemented is missing GRAMMAR, which is a\n",
        "\t// different thing from a builder it cannot describe.\n",
        "\tSyntaxFunctions map[string]struct{}\n",
        "\t// TableFunctions are names that are NOT a Table in a FROM clause:\n",
        "\t// `FROM UNNEST(x)` is an Unnest. The port has no node for these,\n",
        "\t// and wrapping the call in a Table round-trips perfectly while\n",
        "\t// being a different tree, so they are refused. PROBED.\n",
        "\tTableFunctions map[string]struct{}\n",

        "\t// SyntaxSQL is how each of those is WRITTEN, one template per set\n",
        "\t// of present arguments, rendered from the reference.\n",
        "\tSyntaxSQL map[string][]SyntaxTemplate\n",

        "\tFunctionsByArity map[string]map[int]FuncSpec\n",
        "\tClassSensitiveArgs map[string][]ClassTrigger\n",
        "\t// TimeMapping is the dialect\u2019s time-format spelling, and\n",
        "\t// TimeFormatArgs the argument positions it applies to. T-SQL\n",
        "\t// writes yyyy-MM-dd where the reference stores %Y-%m-%d, and the\n",
        "\t// builder rewrites the literal on the way in.\n",
        "\tTimeMapping    map[string]string\n",
        "\tTimeFormatArgs map[string][]int\n",
        "\tCastSensitiveArgs map[string]map[int][]string\n",
        "\t// ZeroSensitiveArgs is the same idea for a NUMBER rather than a\n",
        "\t// cast: DuckDB drops a zero group from REGEXP_EXTRACT entirely.\n",
        "\t// Kept apart from the cast trigger, because a call that moves for\n",
        "\t// one says nothing about the other.\n",
        "\tZeroSensitiveArgs map[string]map[int][]string\n",

        "\tStringSensitiveArgs map[string][]int\n",
        "\t// WritesBooleanLiteral: whether TRUE and FALSE are written as\n",
        "\t// themselves. T-SQL has no boolean literal -- the reference\n",
        "\t// rewrites them to 1 and 0, and to `1 = 1` in a condition. That is\n",
        "\t// a transform, not a spelling, and it is not ported; writing TRUE\n",
        "\t// anyway sent an engine SQL it rejects. PROBED.\n",
        "\t// ReservedKeywords must be QUOTED when written as an identifier\n",
        "\t// even though the caller wrote them bare. DuckDB reserves `all`, so\n",
        '\t// `SELECT 1 AS all` is written `AS "all"`; bare it is a syntax\n',
        "\t// error on the engine.\n",
        "\tReservedKeywords map[string]bool\n",
        "\t// BracketIsRewritten: DuckDB and PostgreSQL shift a subscript to\n",
        "\t// sqlglot\u2019s 0-based Bracket, annotate it, and run the shift\n",
        "\t// through the SIMPLIFIER. That is the optimizer, which is not\n",
        "\t// ported, so where this is set the subscript is REFUSED rather\n",
        "\t// than built plainly. PROBED.\n",
        "\t// ArrayOpen and ArrayClose are the text around an array literal:\n",
        "\t// `[`/`]` in DuckDB, `ARRAY[`/`]` in PostgreSQL, `ARRAY(`/`)`\n",
        "\t// elsewhere. PROBED.\n",
        "\tArrayOpen  string\n",
        "\tArrayClose string\n",
        "\t// IntervalUnitInsideString: PostgreSQL writes INTERVAL \u20181 DAY\u2019\n",
        "\t// with the unit inside the quantity; everyone else writes\n",
        "\t// INTERVAL \u20181\u2019 DAY. PROBED.\n",
        "\tIntervalUnitInsideString bool\n",
        "\t// JSONArrowOnlyJSONTypes and JSONArrowScalarOnly are stamped on\n",
        "\t// the node `->` and `->>` build. PROBED.\n",
        "\tJSONArrowOnlyJSONTypes bool\n",
        "\t// JSONArrowSetsScalarOnly says whether the arg is PRESENT on the\n",
        "\t// node at all; PostgreSQL leaves it off and the others set false.\n",
        "\tJSONArrowSetsScalarOnly bool\n",
        "\t// JSONPathIsParsed: PostgreSQL keeps the string after `->` whole\n",
        "\t// as a single key; everyone else parses it into path parts.\n",
        "\tJSONPathIsParsed bool\n",
        "\t// WritesIntoUnlogged: PostgreSQL keeps UNLOGGED before the table\n",
        "\t// in SELECT ... INTO; T-SQL has no such kind and drops it.\n",
        "\tWritesIntoUnlogged bool\n",
        "\t// JSONPath is how this dialect writes a path, in pieces: DuckDB\n",
        "\t// quotes the whole thing and keeps the $ root, Databricks does\n",
        "\t// neither, and the two spell a key that needs quoting differently.\n",
        "\t// PROBED by subtraction.\n",
        "\tJSONPath JSONPathSQL\n",
        "\t// JSONExtractSQL is how the operator wraps a path, where the\n",
        "\t// dialect writes it as one. A dialect that explodes the path into\n",
        "\t// arguments has no entry and is refused.\n",
        "\tJSONExtractSQL map[string]string\n",

        "\tBracketIsRewritten bool\n",
        "\t// Boolean is how TRUE and FALSE are written, and the two\n",
        "\t// positions can differ: T-SQL writes 1 where a value is wanted\n",
        "\t// and (1 = 1) where a condition is.\n",
        "\t// QuantifierSQL is the text before a quantifier\u2019s operand: ALL\n",
        "\t// takes a trailing space and ANY does not.\n",
        "\tQuantifierSQL map[string]string\n",
        "\t// WritesNullsOrdering: whether NULLS FIRST/LAST is written back.\n",
        "\t// T-SQL has no such clause and drops it.\n",
        "\t// DefaultNullsFirst* is where NULLs sort when the statement does\n",
        "\t// not say, per direction. Read off the reference rather than\n",
        "\t// derived twice -- deriving it in the generator as well as the\n",
        "\t// parser got Databricks wrong.\n",
        "\tDefaultNullsFirstAsc  bool\n",
        "\tDefaultNullsFirstDesc bool\n",
        "\tWritesNullsOrdering bool\n",
        "\tBoolean BooleanSQL\n",
        "\tWritesBooleanLiteral bool\n",
        "\tIsNotNullWrapsInNot bool\n",
        "\t// NullOrdering decides where NULLs sort when nobody says, and so\n",
        "\t// what nulls_first records on an Ordered node. It differs per\n",
        "\t// dialect and is not derivable from ASC or DESC alone.\n",
        "\tNullOrdering string\n",
        "\t// A trailing ORDER BY, LIMIT or OFFSET after a set operation belongs to\n",
        "\t// the set operation, not to the query on its right. The reference\n",
        "\t// parses it onto the right-hand query and then moves it up; which of\n",
        "\t// the three move differs per dialect.\n",
        "\tModifiersAttachedToSetOp bool\n",
        "\tSetOpModifiers           []string\n",
        "\t// The precedence chain, level by level, token to node class. These are\n",
        "\t// per dialect and not interchangeable: DuckDB reads ^ as Pow where the\n",
        "\t// default reads it as BitwiseXor.\n",
        "\tDisjunction map[TokenType]string\n",
        "\tConjunction map[TokenType]string\n",
        "\tEquality    map[TokenType]string\n",
        "\tComparison  map[TokenType]string\n",
        "\tBitwise     map[TokenType]string\n",
        "\tTerm        map[TokenType]string\n",
        "\tFactor      map[TokenType]string\n",
        "\tExponent    map[TokenType]string\n",
        "\t// JoinSides, JoinKinds and JoinMethods are the words that may precede\n",
        "\t// JOIN. A method -- NATURAL, ASOF, POSITIONAL -- changes what the join\n",
        "\t// means and is refused rather than dropped.\n",
        "\tJoinSides   map[TokenType]struct{}\n",
        "\tJoinKinds   map[TokenType]struct{}\n",
        "\tJoinMethods map[TokenType]struct{}\n",
        "\t// RangeTokens are the operators the reference handles at the range\n",
        "\t// level -- IS, IN, BETWEEN, the LIKE family and a dozen operators\n",
        "\t// specific to one dialect. The ones not handled are refused here\n",
        "\t// rather than left to look like the end of the expression.\n",
        "\tRangeTokens map[TokenType]struct{}\n",
        "\t// TypeTokens maps a type keyword to the DataType.Type member the\n",
        "\t// reference records. A few type tokens have no member and are absent,\n",
        "\t// which refuses them rather than inventing one.\n",
        "\tTypeTokens map[TokenType]string\n",
        "}\n\n",
        "// FuncSpec is how one function name becomes a node: the class, and what\n",
        "// fills each of its argument keys.\n",
        "type FuncSpec struct {\n",
        "\tClass string\n",
        "\tArgs  []FuncArg\n",
        "}\n\n",
        "// FuncArg fills one key: a positional argument, a variadic tail that\n",
        "// collects everything from Index onward, or a constant the builder always\n",
        "// sets -- COUNT is always big_int, whatever it was called with.\n",
        "// FuncSQL is one way a function node may be written: the keyword, the\n",
        "// argument keys that become its arguments in order, and the constant\n",
        "// arguments that select this spelling over another. T-SQL writes a\n",
        "// Count as COUNT_BIG when big_int is set and COUNT when it is not.\n",
        "type FuncSQL struct {\n",
        "\tName     string\n",
        "\tKeys     []string\n",
        "\tConsts   []FuncConst\n",
        "\tNoParens bool\n",
        "}\n\n",
        "// FuncConst is an argument that must hold this value for the spelling\n",
        "// beside it to apply.\n",
        "type FuncConst struct {\n",
        "\tKey   string\n",
        "\tValue any\n",
        "}\n\n",
        "// ClassTrigger names the classes that, at Index, make a builder produce\n",
        "// something other than what the probe recorded.\n",
        "// SyntaxTemplate is how one shape of a syntax function is written,\n",
        "// with {key} where each argument goes.\n",
        "// BooleanSQL is how a boolean is written in each position.\n",
        "type BooleanSQL struct {\n",
        "\tTrueValue      string\n",
        "\tFalseValue     string\n",
        "\tTrueCondition  string\n",
        "\tFalseCondition string\n",
        "}\n",
        "\n",
        "// JSONPathSQL is the text around each piece of a JSON path.\n",
        "type JSONPathSQL struct {\n",
        "\tOpen      string\n",
        "\tClose     string\n",
        "\tKey       string\n",
        "\tSubscript string\n",
        "\tQuotedKey string\n",
        "}\n",
        "\n",
        "type SyntaxTemplate struct {\n",
        "\tKeys     []string\n",
        "\tMarked   []string\n",
        "\tRequired []FuncConst\n",
        "\tTemplate string\n",
        "}\n",
        "\n",
        "type ClassTrigger struct {\n",
        "\tIndex   int\n",
        "\tClasses []string\n",
        "}\n",
        "\n",
        "type FuncArg struct {\n",
        "\tKey    string\n",
        "\tIndex  int\n",
        "\tVarLen bool\n",
        "\tConst  any\n",
        "\t// Wrap names a class to build FROM the argument rather than to hold\n",
        "\t// it: DATEADD records unit=Var(args[Index].name upper-cased), and the\n",
        "\t// argument itself appears nowhere in the result. 28 names do this,\n",
        "\t// and every one of them used to be refused outright.\n",
        "\tWrap   string\n",
        "\t// WrapArgs are the wrapper\u2019s other arguments -- a Literal always\n",
        "\t// carries is_string, and a check for exactly one argument rejected\n",
        "\t// every builder whose wrapper was a Literal rather than a Var.\n",
        "\tWrapArgs []FuncConst\n",

        "}\n\n",
        "var parserTables = map[string]*ParserTables{\n",
    ]
    for name in DIALECTS:
        P = Dialect.get_or_raise(name or None).parser_class
        named = set(P.FUNCTIONS) | set(P.FUNCTION_PARSERS)
        out.append(f"\t{gostr(name)}: {{\n")
        out.append(ttset("IDVarTokens", P.ID_VAR_TOKENS))
        out.append(ttset("TableAliasTokens", P.TABLE_ALIAS_TOKENS))
        out.append(strset("NamedFunctions", named))
        out.append(ttset("NoParenFunctions", P.NO_PAREN_FUNCTIONS))
        out.append(opmap("NoParenFunctionClasses", P.NO_PAREN_FUNCTIONS))
        # Names with their own SYNTAX, not merely their own builder:
        # EXTRACT(unit FROM x), TRIM(BOTH ' ' FROM x), POSITION(a IN b). The
        # port used to lump these in with unportable builders, which filed
        # missing GRAMMAR under a label that reads as "cannot be ported" -- and
        # that label is what decides what gets built next.
        out.append(strset("SyntaxFunctions", sorted(P.FUNCTION_PARSERS)))
        out.append(strset("TableFunctions", table_functions(name, P)))
        funcs, by_arity, unit_maps = probe_functions(P, exp, name)
        if unit_maps:
            out.append("\t\tUnitAliases: map[string]map[string]string{\n")
            for fname in sorted(unit_maps):
                pairs = "".join(
                    f"{gostr(k)}: {gostr(v)}, " for k, v in sorted(unit_maps[fname].items())
                )
                out.append(f"\t\t\t{gostr(fname)}: {{{pairs}}},\n")
            out.append("\t\t},\n")
        out.append(funcmap("Functions", funcs))
        if by_arity:
            out.append("\t\tFunctionsByArity: map[string]map[int]FuncSpec{\n")
            for fname in sorted(by_arity):
                out.append(f"\t\t\t{gostr(fname)}: {{\n")
                for arity in sorted(by_arity[fname]):
                    cls, spec = by_arity[fname][arity]
                    out.append(f"\t\t\t\t{arity}: {{{gostr(cls)}, []FuncArg{{{funcargs(spec)}}}}},\n")
                out.append("\t\t\t},\n")
            out.append("\t\t},\n")
        # The generator needs a spelling for these too; the widest arity gives
        # it one, and render_functions probes the narrower forms itself.
        render_input = dict(funcs)
        for fname, variants in by_arity.items():
            render_input.setdefault(fname, variants[max(variants)])
        classes = class_sensitive_args(P, exp, name, render_input)
        if classes:
            out.append("\t\tClassSensitiveArgs: map[string][]ClassTrigger{\n")
            for fname, byindex in sorted(classes.items()):
                triggers = ", ".join(
                    "{%d, []string{%s}}" % (i, ", ".join(gostr(c) for c in cs))
                    for i, cs in sorted(byindex.items())
                )
                out.append(f"\t\t\t{gostr(fname)}: {{{triggers}}},\n")
            out.append("\t\t},\n")
        syn = syntax_templates(exp, name, pathlib.Path(__file__).resolve().parent.parent)
        if syn:
            out.append("\t\tSyntaxSQL: map[string][]SyntaxTemplate{\n")
            for cls_name in sorted(syn):
                entries = "".join(
                    "{[]string{%s}, []string{%s}, []FuncConst{%s}, %s}, "
                    % (
                        ", ".join(gostr(k) for k in keys),
                        ", ".join(gostr(k) for k in marked),
                        "".join("{%s, %s}, " % (gostr(k), gostr(v)) for k, v in required),
                        gostr(text),
                    )
                    for keys, marked, required, text in syn[cls_name]
                )
                out.append(f"\t\t\t{gostr(cls_name)}: {{{entries}}},\n")
            out.append("\t\t},\n")
        from sqlglot.dialects.dialect import Dialect as _D
        _tm = getattr(_D.get_or_raise(name or None), "TIME_MAPPING", None) or {}
        if _tm:
            out.append("\t\tTimeMapping: map[string]string{\n")
            for k in sorted(_tm):
                out.append(f"\t\t\t{gostr(k)}: {gostr(_tm[k])},\n")
            out.append("\t\t},\n")
        _tfa = time_format_args(name, P, exp, render_input)
        if _tfa:
            out.append("\t\tTimeFormatArgs: map[string][]int{\n")
            for fname in sorted(_tfa):
                joined = ", ".join(str(i) for i in _tfa[fname])
                out.append(f"\t\t\t{gostr(fname)}: {{{joined}}},\n")
            out.append("\t\t},\n")
        casts, zeros = cast_sensitive_args(P, exp, name, render_input)
        for field, table in (("ZeroSensitiveArgs", zeros), ("CastSensitiveArgs", casts)):
            if not table:
                continue
            out.append(f"\t\t{field}: map[string]map[int][]string{{\n")
            for fname in sorted(table):
                inner = "".join(
                    "%d: {%s}, " % (arity, ", ".join(gostr(k) for k in keys))
                    for arity, keys in sorted(table[fname].items())
                )
                out.append(f"\t\t\t{gostr(fname)}: {{{inner}}},\n")
            out.append("\t\t},\n")
        sensitive = string_sensitive_args(P, exp, render_input, name)
        if sensitive:
            out.append("\t\tStringSensitiveArgs: map[string][]int{\n")
            for fname, indexes in sorted(sensitive.items()):
                joined = ", ".join(str(i) for i in indexes)
                out.append(f"\t\t\t{gostr(fname)}: {{{joined}}},\n")
            out.append("\t\t},\n")
        out.append(sqlmap("FunctionSQL", render_functions(P, exp, name, render_input)))
        binary, unary = render_operators(P, exp, Dialect, name)
        out.append(strstrmap("BinarySQL", binary))
        out.append(strstrmap("UnarySQL", unary))
        types_sql = {}
        for t, member in sorted(
            {t: exp.DType[t.name] for t in P.TYPE_TOKENS if t.name in exp.DType.__members__}.items(),
            key=lambda kv: kv[0].value,
        ):
            try:
                rendered = exp.DataType(this=member).sql(dialect=name or None)
            except Exception:  # noqa: BLE001
                continue
            types_sql[member.value] = rendered
        out.append(strstrmap("TypeSQL", types_sql))
        quoted = exp.Identifier(this="A", quoted=True).sql(dialect=name or None)
        out.append(f"\t\tIdentifierStart: {gostr(quoted[0])},\n")
        out.append(f"\t\tIdentifierEnd: {gostr(quoted[-1])},\n")
        # T-SQL requires every column of a derived table to be named, and
        # synthesises the missing aliases on the way out. Detected by asking
        # rather than by naming the dialect.
        import sqlglot as _sqlglot  # noqa: PLC0415

        probe = _sqlglot.parse_one("SELECT * FROM (SELECT a) AS x", read=name or None)
        qualifies = " AS a" in probe.sql(dialect=name or None)
        out.append(f"\t\tQualifiesDerivedOutputs: {str(qualifies).lower()},\n")
        # T-SQL has no boolean type, so a value used as a condition is written
        # `x <> 0`. Detected by asking, not by naming the dialect.
        coerces = exp.Not(this=exp.column("x")).sql(dialect=name or None) != "NOT x"
        out.append(f"\t\tCoercesBooleans: {str(coerces).lower()},\n")
        out.append(
            "\t\tLimitIsTop: "
            f"{str(bool(Dialect.get_or_raise(name or None).generator_class.LIMIT_IS_TOP)).lower()},\n"
        )
        tk = Dialect.get_or_raise(name or None).tokenizer_class
        out.append(ttset("StatementTokens", set(P.STATEMENT_PARSERS) | set(tk.COMMANDS)))
        out.append(ttset("FuncTokens", P.FUNC_TOKENS))
        out.append(strset("NoParenFunctionNames", P.NO_PAREN_FUNCTION_PARSERS))
        d = Dialect.get_or_raise(name or None)
        for field, value in (
            ("TypedDivision", d.TYPED_DIVISION),
            ("SafeDivision", d.SAFE_DIVISION),
            ("DPipeIsStringConcat", d.DPIPE_IS_STRING_CONCAT),
            ("StrictStringConcat", d.STRICT_STRING_CONCAT),
            ("JoinsHaveEqualPrecedence", P.JOINS_HAVE_EQUAL_PRECEDENCE),
        ):
            out.append(f"\t\t{field}: {str(bool(value)).lower()},\n")
        out.append(
            f"\t\tBareJoinIsOnTrue: {str(bare_join_is_on_true(name)).lower()},\n"
        )
        defaults = default_type_params(name)
        if defaults:
            out.append("\t\tDefaultTypeParams: map[string][]string{\n")
            for typ, params in sorted(defaults.items()):
                joined = ", ".join(gostr(v) for v in params)
                out.append(f"\t\t\t{gostr(typ)}: {{{joined}}},\n")
            out.append("\t\t},\n")
        drops = drops_type_params(name)
        if drops:
            out.append("\t\tDropsTypeParams: map[string]bool{\n")
            for typ in drops:
                out.append(f"\t\t\t{gostr(typ)}: true,\n")
            out.append("\t\t},\n")
        out.append(
            f"\t\tLimitAllMeansNoLimit: {str(limit_all_means_no_limit(name)).lower()},\n"
        )
        reserved = reserved_keywords(name, Dialect)
        if reserved:
            out.append("\t\tReservedKeywords: map[string]bool{\n")
            for w in reserved:
                out.append(f"\t\t\t{gostr(w)}: true,\n")
            out.append("\t\t},\n")
        _oj, _so = json_arrow_flags(name)
        out.append(f"\t\tJSONArrowOnlyJSONTypes: {str(_oj).lower()},\n")
        out.append(f"\t\tJSONArrowSetsScalarOnly: {str(_so).lower()},\n")
        out.append(
            f"\t\tJSONPathIsParsed: {str(json_path_is_parsed(name)).lower()},\n"
        )
        out.append(
            f"\t\tIntervalUnitInsideString: {str(interval_unit_inside_string(name, exp)).lower()},\n"
        )
        _ao, _ac = array_delimiters(name, exp)
        out.append(f"\t\tArrayOpen: {gostr(_ao)},\n")
        out.append(f"\t\tArrayClose: {gostr(_ac)},\n")
        out.append(
            f"\t\tWritesIntoUnlogged: {str(writes_into_unlogged(name, exp)).lower()},\n"
        )
        _je = json_extract_sql(name, exp)
        if _je:
            out.append("\t\tJSONExtractSQL: map[string]string{\n")
            for k in sorted(_je):
                out.append(f"\t\t\t{gostr(k)}: {gostr(_je[k])},\n")
            out.append("\t\t},\n")
        _jp = json_path_pieces(name, exp)
        if _jp:
            out.append("\t\tJSONPath: JSONPathSQL{\n")
            for k in ("Open", "Close", "Key", "Subscript", "QuotedKey"):
                out.append(f"\t\t\t{k}: {gostr(_jp[k])},\n")
            out.append("\t\t},\n")
        out.append(
            f"\t\tBracketIsRewritten: {str(bracket_is_rewritten(name)).lower()},\n"
        )
        _q = quantifier_sql(name, exp)
        if _q:
            out.append("\t\tQuantifierSQL: map[string]string{\n")
            for k in sorted(_q):
                out.append(f"\t\t\t{gostr(k)}: {gostr(_q[k])},\n")
            out.append("\t\t},\n")
        _na, _nd = default_nulls_first(name)
        out.append(f"\t\tDefaultNullsFirstAsc: {str(_na).lower()},\n")
        out.append(f"\t\tDefaultNullsFirstDesc: {str(_nd).lower()},\n")
        out.append(
            f"\t\tWritesNullsOrdering: {str(writes_nulls_ordering(name)).lower()},\n"
        )
        _bs = boolean_sql(name, exp)
        out.append("\t\tBoolean: BooleanSQL{\n")
        for k in ("TrueValue", "FalseValue", "TrueCondition", "FalseCondition"):
            out.append(f"\t\t\t{k}: {gostr(_bs[k])},\n")
        out.append("\t\t},\n")
        out.append(
            f"\t\tWritesBooleanLiteral: {str(writes_boolean_literal(name, exp)).lower()},\n"
        )
        out.append(
            f"\t\tIsNotNullWrapsInNot: "
            f"{str(is_not_null_wraps_in_not(name)).lower()},\n"
        )
        out.append(f"\t\tNullOrdering: {gostr(d.NULL_ORDERING)},\n")
        out.append(
            f"\t\tModifiersAttachedToSetOp: {str(bool(P.MODIFIERS_ATTACHED_TO_SET_OP)).lower()},\n"
        )
        mods = "".join(f"{gostr(m)}, " for m in sorted(P.SET_OP_MODIFIERS))
        out.append(f"\t\tSetOpModifiers: []string{{{mods}}},\n")
        for field, table in (
            ("Disjunction", P.DISJUNCTION),
            ("Conjunction", P.CONJUNCTION),
            ("Equality", P.EQUALITY),
            ("Comparison", P.COMPARISON),
            ("Bitwise", P.BITWISE),
            ("Term", P.TERM),
            ("Factor", P.FACTOR),
            ("Exponent", P.EXPONENT),
        ):
            out.append(opmap(field, table))
        out.append(ttset("JoinSides", P.JOIN_SIDES))
        out.append(ttset("JoinKinds", P.JOIN_KINDS))
        out.append(ttset("JoinMethods", P.JOIN_METHODS))
        out.append(ttset("RangeTokens", P.RANGE_PARSERS))
        types = {t: exp.DType[t.name] for t in P.TYPE_TOKENS if t.name in exp.DType.__members__}
        body = "".join(
            f"\t\t\tTok{t.name}: {gostr(v.value)},\n"
            for t, v in sorted(types.items(), key=lambda kv: kv[0].value)
        )
        out.append(f"\t\tTypeTokens: map[TokenType]string{{\n{body}\t\t}},\n")
        out.append("\t},\n")
    out.append("}\n")

    a.out.write_text("".join(out))
    gofmt(a.out)
    print(f"reference {actual[:12]}: wrote {a.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
