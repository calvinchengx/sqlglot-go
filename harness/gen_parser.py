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


# The types a dispatching builder is probed against. Small on purpose: the
# point is to find WHICH type it singles out, not to enumerate the type
# system. DuckDB's DATE_TRUNC builds a DateTrunc for a DATE and a
# TimestampTrunc for everything else, including a bare column.
DISPATCH_TYPES = ("DATE", "TIMESTAMP", "DATETIME", "TEXT", "INT")


def _type_dispatch(exp, builder, args, index, dialect, describe, call_builder):
    """A builder that picks its CLASS from one argument's TYPE.

    Probed by casting that argument to each of a few types and reading back
    what was built. The result is a spec PER type -- not just a class name --
    because the shapes differ: DuckDB's DateTrunc puts the unit first and its
    TimestampTrunc puts the value first, and a dump compares key order.

    Recorded only when every type yields a describable spec and at least two
    of them disagree. A builder that varies for some other reason yields
    nothing here and stays refused, as it was before.
    """
    plain = call_builder(builder, args, dialect)
    by_type: dict[str, tuple] = {}
    for ty in DISPATCH_TYPES:
        subst = list(args)
        subst[index] = exp.cast(exp.column(f"__probe_{index}"), ty)
        try:
            node = call_builder(builder, subst, dialect)
        except Exception:  # noqa: BLE001 -- this type is invalid here
            return None
        if not isinstance(node, exp.Expr):
            return None
        spec = describe(node, subst)
        if spec is None:
            return None
        by_type[ty] = (type(node).__name__, spec)

    default = (type(plain).__name__, describe(plain, args))
    if default[1] is None:
        return None
    if len({cls for cls, _ in by_type.values()} | {default[0]}) < 2:
        return None
    # Only the types that DIFFER from the default are worth recording.
    special = {ty: v for ty, v in by_type.items() if v[0] != default[0]}
    if not special:
        return None
    return {"index": index, "default": default, "by_type": special}


def json_path_functions(P, exp, dialect=""):
    """The names that turn their arguments into a JSON PATH.

    Five of them across these dialects, and the generic probe rejects every
    one: it feeds placeholder COLUMNS, and these builders look at what they
    were handed. With columns they fall back to a plain positional shape, so
    the spec they yield is right for columns and wrong for the literals real
    SQL contains -- which is exactly what the substitution check catches, and
    why they were all filed under "a builder of its own".

    So they are probed with STRING literals instead, and the shape is read off
    what comes back. Two shapes exist and four arguments tell them apart:

      JSON_EXTRACT(x, 'a', 'b', 'c')       PATH[Root, Key(a)]        parse one
      JSON_EXTRACT_PATH(x, 'a', 'b', 'c')  PATH[Root, Key(a), Key(b), Key(c)]

    The first parses ONE argument as a path string. The second folds all of
    them into a path, one segment each. A probe with two arguments cannot see
    the difference -- both give a single key -- which is why it uses four.
    """
    out = {}
    for name, builder in P.FUNCTIONS.items():
        if name in P.FUNCTION_PARSERS:
            continue
        args = [exp.column("__probe_0")] + [
            exp.Literal.string("__probe_%d" % i) for i in (1, 2, 3)
        ]
        try:
            node = call_builder(builder, list(args), dialect)
        except Exception:  # noqa: BLE001 -- not a name that takes four
            continue
        if not isinstance(node, exp.Expr):
            continue
        path = node.args.get("expression")
        if not isinstance(path, exp.JSONPath):
            continue
        segs = path.expressions
        if not segs or not isinstance(segs[0], exp.JSONPathRoot):
            continue
        keys = [
            seg.this
            for seg in segs[1:]
            if isinstance(seg, exp.JSONPathKey)
        ]
        if keys == ["__probe_1", "__probe_2", "__probe_3"]:
            fold = True
        elif keys == ["__probe_1"]:
            fold = False
        else:
            continue
        if node.args.get("this") is not args[0]:
            continue
        # Whatever else the builder stamps on, as a constant. only_json_types
        # rides on all of these and json_type on some.
        consts = [
            (k, v)
            for k, v in node.args.items()
            if k not in ("this", "expression", "expressions")
            and (v is None or isinstance(v, (bool, str, int)))
        ]
        # Does an argument the builder did not fold survive? DuckDB keeps the
        # extras under `expressions`; Databricks DROPS them, and dropping an
        # argument changes what the call means, so the port refuses instead.
        tail = node.args.get("expressions")
        keeps_tail = bool(tail) and not fold
        entry = {
            "class": type(node).__name__,
            "fold": fold,
            "consts": consts,
            "keeps_tail": keeps_tail,
        }
        if fold:
            entry.update(_json_fold_rules(builder, exp, dialect))
        # With no path argument at all, does the builder still make a node?
        # T-SQL's JSON_QUERY(x) means the whole document and gets a bare root.
        entry["root_default"] = False
        try:
            lone = call_builder(builder, [exp.column("__probe_0")], dialect)
            path = lone.args.get("expression")
            entry["root_default"] = (
                isinstance(path, exp.JSONPath)
                and len(path.expressions) == 1
                and isinstance(path.expressions[0], exp.JSONPathRoot)
            )
        except Exception:  # noqa: BLE001 -- this name needs its path
            pass
        out[name] = entry
    return out


def _json_fold_rules(builder, exp, dialect):
    """Whether a folded key that reads as an integer becomes a SUBSCRIPT.

    `JSON_EXTRACT_PATH(x, '0')` indexes rather than naming a key called "0",
    and whether the index is taken as written is a flag baked into the
    builder's closure where nothing can read it. So it is asked: hand it a
    5 and see what number comes back.
    """
    rules = {"int_subscripts": False, "index_shift": 0}
    try:
        node = call_builder(
            builder, [exp.column("__probe_0"), exp.Literal.string("5")], dialect
        )
        seg = node.args["expression"].expressions[1]
    except Exception:  # noqa: BLE001
        return rules
    if isinstance(seg, exp.JSONPathSubscript):
        rules["int_subscripts"] = True
        rules["index_shift"] = 5 - int(seg.this)
    return rules


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

    dispatch: dict = {}

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
                wrapped = None
                if isinstance(inner, str) and len(extras) == len(value.args) - 1:
                    for i, a in enumerate(args):
                        if inner == a.name.upper():
                            wrapped = {
                                "wrap": type(value).__name__,
                                "index": i,
                                "extras": extras,
                            }
                            break
                if wrapped is not None:
                    out.append((key, wrapped))
                elif all(
                    v is None or isinstance(v, (bool, str, int))
                    for v in value.args.values()
                ):
                    # No argument reached this node and everything in it is a
                    # scalar: a CONSTANT node the builder always supplies.
                    # DuckDB's two-argument REGEXP_EXTRACT_ALL fills group with
                    # Literal('0'); LOG10 sets this=Literal('10'). The
                    # scalar-const branch above cannot hold it -- a Literal is
                    # an Expr, not a scalar -- so the whole builder used to be
                    # refused. The test has to come AFTER the wrapper one: run
                    # first it captures DATEADD's Var(unit) as whatever unit
                    # the probe happened to pass.
                    out.append(
                        (
                            key,
                            {
                                "node": type(value).__name__,
                                "extras": list(value.args.items()),
                            },
                        )
                    )
                else:
                    return None
            else:
                return None
        return out

    def rebuild(spec, args):
        out = {}
        for key, how in spec:
            if "node" in how:
                out[key] = getattr(exp, how["node"])(**dict(how.get("extras") or []))
            elif "wrap" in how:
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
        varied_at = None
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
                    # The CLASS changed with this argument. That may be a
                    # builder nobody can describe -- or a dispatch on the
                    # argument's TYPE, which the port can now follow because
                    # it has an annotator. Remembered, and tried below.
                    ok = False
                    varied_at = i
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
        if not ok and varied_at is not None:
            entry = _type_dispatch(
                exp, builder, args, varied_at, dialect, describe, call_builder
            )
            if entry is not None:
                dispatch[name] = entry
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
    return out, by_arity, unit_maps, dispatch


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


def within_group_folding_names(dialect: str, P) -> list:
    """Which function NAMES swallow a `WITHIN GROUP (ORDER BY ...)`.

    It belongs to the name's builder, not to the class it builds: Databricks
    reads LISTAGG into a GroupConcat and still does NOT fold, where STRING_AGG
    into the same class does. Keyed by class, the port folded both and put a
    GroupConcat where the reference has a WithinGroup.
    """
    import sqlglot

    out = []
    # Both tables: STRING_AGG has a PARSER rather than a signature, and it is
    # the one that folds in most dialects.
    for name in sorted(set(P.FUNCTIONS) | set(P.FUNCTION_PARSERS)):
        if not name.isidentifier():
            continue
        try:
            node = sqlglot.parse_one(
                "%s(x, z) WITHIN GROUP (ORDER BY y)" % name, read=dialect or None
            )
        except Exception:  # noqa: BLE001 -- not a call this name makes
            continue
        if type(node).__name__ != "WithinGroup":
            out.append(name)
    return out


def create_as_select_rewritten(dialect: str) -> bool:
    """Whether `CREATE TABLE x AS SELECT` is written back as something else.

    T-SQL has no such statement and the reference REWRITES it into
    `SELECT * INTO x FROM (...)`, wrapping the query and naming it. That is a
    transformation rather than a spelling, and this port does not do it -- so
    the statement is refused there rather than written as itself.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE TABLE x AS SELECT 1", read=dialect or None).sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return True
    return not text.upper().startswith("CREATE")


def map_brace_literal(dialect: str) -> bool:
    """Whether `MAP {k: v}` is a map LITERAL in this dialect.

    DuckDB's. Elsewhere MAP is an ordinary name and the braces are a struct,
    so applying the rule everywhere read `MAP{:YD00:0}` as a map and wrote
    something PostgreSQL cannot parse. The generator fuzzer found it.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one("SELECT MAP {'x': 1}", read=dialect or None).selects[0]
    except Exception:  # noqa: BLE001
        return False
    return type(node).__name__ == "ToMap"


def group_concat_order(dialect: str) -> str:
    """Where a folded ORDER BY goes when a GroupConcat is written back.

    `STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y)` FOLDS into a GroupConcat
    whose first argument is the ordering of x. Writing it back, the dialects
    disagree about where the ordering goes: inside the first argument, after
    the separator, or unfolded into a WITHIN GROUP again. Only the two the
    port can spell are named; anything else is refused rather than written in
    the wrong place.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one(
            "SELECT STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)"
        ).selects[0]
        text = node.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ""
    if "WITHIN GROUP" in text:
        return "within_group"
    if "x ORDER BY y DESC" in text:
        return "inline"
    return ""


def select_sample_words(dialect: str, exp) -> tuple:
    """What a sample is called where it hangs off the QUERY rather than a table.

    DuckDB writes the same node `TABLESAMPLE ...` after a table and
    `USING SAMPLE ...` after the query. One node, two words, decided by where
    it is -- so both are read off a rendering rather than assumed equal.
    """
    import sqlglot

    node = exp.TableSample(
        method=exp.Var(this="RESERVOIR"), size=exp.Literal.number(5)
    )
    try:
        standalone = (
            sqlglot.dialects.dialect.Dialect.get_or_raise(dialect or None)
            .generator()
            .sql(node)
        )
        select = sqlglot.parse_one("SELECT * FROM t", read=dialect or None)
        select.set("sample", node.copy())
        rendered = select.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ("", "")
    tail = standalone.strip()
    # The two spellings share everything after the leading words. A dialect
    # that writes no method at all has nothing to line them up on.
    if "RESERVOIR" not in tail or "RESERVOIR" not in rendered:
        return ("", "")
    marker = tail[tail.index("RESERVOIR") :]
    if marker not in rendered:
        return ("", "")
    head = rendered[: rendered.index(marker)]
    head = head[len("SELECT * FROM t ") :]
    return (tail[: tail.index("RESERVOIR")].strip(), head.strip())


def for_clause_options(dialect: str) -> dict:
    """The option vocabulary of `FOR XML` and `FOR JSON`.

    Each kind has a table of words, and a word may take a second word after it
    -- `ELEMENTS XSINIL`, `BINARY BASE64`. A word IN the table becomes a plain
    Var; one that is not falls through to a key/value option, which is why
    `PATH` is a Var under JSON, where the table has it, and an
    XMLKeyValueOption under XML, where it does not.

    Read off the reference's own tables rather than transcribed, and empty for
    every dialect that has none.
    """
    import importlib

    out = {}
    for kind in ("XML", "JSON"):
        try:
            module = importlib.import_module("sqlglot.parsers." + (dialect or "_"))
            table = getattr(module, "FOR_%s_OPTIONS" % kind, None)
        except Exception:  # noqa: BLE001 -- no parser module for this dialect
            table = None
        if not table:
            continue
        out[kind] = {word: list(follows) for word, follows in table.items()}
    return out


def version_range_separators(dialect: str, exp) -> dict:
    """The word between the two bounds of a FOR SYSTEM_TIME range.

    The bounds are held as a Tuple, which would render `(c, d)`, and T-SQL
    writes `c TO d` for FROM and `c AND d` for BETWEEN instead. The word
    depends on the KIND and lives in the dialect's writer, so it is read back
    off a rendering rather than transcribed.
    """
    out = {}
    for kind in ("FROM", "BETWEEN", "CONTAINED IN"):
        node = exp.Version(
            this="TIMESTAMP",
            expression=exp.Tuple(
                expressions=[exp.column("ZZLOWZZ"), exp.column("ZZHIGHZZ")]
            ),
            kind=kind,
        )
        try:
            text = node.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        head = text.find("ZZLOWZZ")
        tail = text.find("ZZHIGHZZ")
        if head < 0 or tail < 0 or tail < head:
            continue
        between = text[head + len("ZZLOWZZ") : tail].strip()
        # A comma means the tuple was written as a tuple, which needs no
        # separator of its own.
        if between and between != ",":
            out[kind] = between
    return out


def pivot_conventions(dialect: str) -> dict:
    """The four things a PIVOT node carries that the statement never says.

    Each is a parser constant with no spelling of its own, so each is read off
    a pivot the reference just built rather than transcribed from the class.
    """
    import sqlglot
    from sqlglot import exp

    out = {
        "naming": "",
        "identify_strings": False,
        "prefixed_columns": False,
        "value_columns_first": False,
    }
    try:
        node = next(
            iter(
                sqlglot.parse_one(
                    "SELECT a FROM t PIVOT(SUM(x) FOR y IN ('z'))", read=dialect or None
                ).find_all(exp.Pivot)
            )
        )
        out["naming"] = node.args.get("pivot_column_naming") or ""
        out["identify_strings"] = bool(node.args.get("identify_pivot_strings"))
        out["prefixed_columns"] = bool(node.args.get("prefixed_pivot_columns"))
    except Exception:  # noqa: BLE001 -- not a form this dialect reads
        pass
    try:
        node = next(
            iter(
                sqlglot.parse_one(
                    "SELECT a FROM t UNPIVOT(x FOR y IN (z))", read=dialect or None
                ).find_all(exp.Pivot)
            )
        )
        out["value_columns_first"] = bool(node.args.get("value_columns_first"))
    except Exception:  # noqa: BLE001
        pass
    return out


def prefix_alias(dialect: str) -> bool:
    """Whether `name: expr` NAMES the expression in this dialect.

    DuckDB writes `SELECT foo: 1` for `SELECT 1 AS foo`. The same characters
    are a JSON extraction in Databricks -- `c1:price` -- so which one a colon
    is depends entirely on the dialect, and both are read here under their own
    flag rather than by guessing from what follows.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one("SELECT foo: 1", read=dialect or None).selects[0]
    except Exception:  # noqa: BLE001 -- not a form this dialect has
        return False
    return type(node).__name__ == "Alias"


def bare_sample_count_is_percent(dialect: str, exp) -> bool:
    """What `TABLESAMPLE (3)` counts, with no unit written after it.

    PostgreSQL reads a bare count as a PERCENTAGE and the others as a number
    of ROWS -- the same three characters meaning two different sizes of
    sample. Nothing declares it, so it is read off a statement that omits the
    unit.
    """
    import sqlglot

    try:
        tree = sqlglot.parse_one(
            "SELECT * FROM t TABLESAMPLE (3)", read=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    node = next(iter(tree.find_all(exp.TableSample)), None)
    return bool(node is not None and node.args.get("percent") is not None)


def default_sample_method(dialect: str, exp) -> str:
    """The sampling method this dialect supplies when none is written.

    DuckDB records RESERVOIR whether or not the statement says so, and the
    others record nothing. It is a default baked into the parser where no flag
    reports it, so it is read off a statement that omits the method.
    """
    import sqlglot

    try:
        tree = sqlglot.parse_one(
            "SELECT * FROM t TABLESAMPLE (3 ROWS)", read=dialect or None
        )
    except Exception:  # noqa: BLE001 -- not a form this dialect reads
        return ""
    node = next(iter(tree.find_all(exp.TableSample)), None)
    method = node.args.get("method") if node else None
    return method.name if method else ""


def json_key_value_sql(dialect: str, exp) -> str:
    """How this dialect writes one key/value pair inside JSON_OBJECT.

    DuckDB separates them with a COMMA -- `JSON_OBJECT('k', 1)` -- and the
    others with a colon. The template machinery cannot hold this one: a
    marker-operator-marker template is rejected there as an infix operator
    whose precedence a template cannot know, which is right for `a #> b` and
    wrong for a pair that is only ever written inside its own parentheses.
    """
    node = exp.JSONKeyValue(
        this=exp.Literal.string("ZZKZZ"), expression=exp.column("ZZVZZ")
    )
    try:
        text = node.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001 -- a dialect that will not write one
        return ""
    if text.count("ZZKZZ") != 1 or text.count("ZZVZZ") != 1:
        return ""
    return text.replace("'ZZKZZ'", "{this}").replace("ZZVZZ", "{expression}")


def variant_extract_colon(dialect: str) -> bool:
    """Whether `x:a` is a JSON extraction in this dialect.

    Databricks spells one that way -- `c1:item[1].price` -- and the port WROTE
    that form while refusing to read a single instance of it, so every
    extraction it emitted for Databricks was SQL it could not read back. The
    generator fuzzer found it on `0->''`.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one("SELECT x:a", dialect=dialect or None).selects[0]
    except Exception:  # noqa: BLE001 -- not a form this dialect has
        return False
    return type(node).__name__ == "JSONExtract" and bool(
        node.args.get("variant_extract")
    )


def json_extract_needs_parens(dialect: str) -> dict:
    """Whether a JSON extraction used as an OPERAND is parenthesised.

    The reference writes `(a -> b) & c` in DuckDB and `a:b & c` in Databricks,
    for the same tree. It is the SPELLING that decides -- an arrow is an
    operator and needs the parentheses, a function call and Databricks' colon
    do not -- and the spelling is per dialect, so the answer is read off the
    reference for each one rather than guessed from the template.

    This became reachable when the arrow moved into the bitwise level, where
    the reference has it. Before that a JSON extraction could never BE the
    operand of a bitwise operator, and the port wrote `a -> b & c`.
    """
    import sqlglot
    from sqlglot import exp

    out = {}
    for cls in ("JSONExtract", "JSONExtractScalar"):
        node = getattr(exp, cls)(
            this=exp.column("__probe_a"),
            expression=exp.JSONPath(
                expressions=[exp.JSONPathRoot(), exp.JSONPathKey(this="__probe_b")]
            ),
        )
        try:
            alone = node.sql(dialect=dialect or None)
            joined = exp.BitwiseAnd(this=node.copy(), expression=exp.column("c")).sql(
                dialect=dialect or None
            )
        except Exception:  # noqa: BLE001 -- a dialect that will not write it
            continue
        out[cls] = joined.startswith("(" + alone + ")")
    return out


def json_arrow_flags(dialect: str) -> tuple[bool, bool, bool]:
    """What `->` and `->>` stamp on the node they build.

    PostgreSQL sets only_json_types. And PostgreSQL alone leaves scalar_only
    OFF the node, where the others set it to False -- so the second value here
    says whether the arg is PRESENT, not what it is.

    The third says whether only_json_types survives an operand that is NOT a
    path. PostgreSQL's arrow runs the same builder its JSON_EXTRACT_PATH does,
    and that builder returns EARLY when it is handed something it cannot read
    -- before it stamps the flag on. DuckDB's stamps it either way. Reading
    the flag alone said they agreed; the trees disagree.
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
    plain = sqlglot.parse_one("SELECT x -> c", dialect=dialect or None).selects[0]
    return (
        bool(getattr(d.parser_class, "JSON_ARROWS_REQUIRE_JSON_TYPE", False)),
        "scalar_only" in node.args,
        "only_json_types" in plain.args,
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


def json_extract_chained(dialect: str, exp) -> dict:
    """How a dialect writes a MULTI-PART path, when it does not write it whole.

    PostgreSQL has no single path literal: it emits one operator per part --
    `j -> 'a' -> 'b'` -- or, when the node is not restricted to JSON types, one
    ARGUMENT per part: `JSON_EXTRACT_PATH(j, 'a', 'b')`. Either way the single
    node becomes N of something, which is a transform rather than a spelling,
    so both forms are read off the reference and folded here.

    Returns, per class, the per-part template and the function form.
    """

    def path(*parts):
        ps = [exp.JSONPathRoot()]
        for part in parts:
            ps.append(exp.JSONPathKey(this=part))
        return exp.JSONPath(expressions=ps)

    out = {}
    for cls_name in ("JSONExtract", "JSONExtractScalar"):
        cls = getattr(exp, cls_name)
        forms = {}
        for flag, key in ((True, "Chain"), (False, "Call")):
            one = cls(this=exp.column("THIS"), expression=path("KEYA"), only_json_types=flag)
            two = cls(
                this=exp.column("THIS"), expression=path("KEYA", "KEYB"), only_json_types=flag
            )
            try:
                a, b = one.sql(dialect=dialect or None), two.sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001
                continue
            if "KEYA" not in a or "KEYB" not in b:
                continue
            forms[key] = (a.replace("THIS", "{this}").replace("KEYA", "{part}"), b)
        if forms:
            out[cls_name] = forms
    return out


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


def json_path_pieces_in_class(dialect: str, exp, cls_name: str) -> dict:
    """The same pieces, as written INSIDE one extraction class.

    A path does not render the same everywhere it appears. Databricks writes
    the very same path as `c1:item[1].price` under JSONExtract and as
    `GET_JSON_OBJECT(c1, '$.item[1].price')` under JSONExtractScalar -- a
    different root, a different separator, a different quoting. Probing a
    STANDALONE path caught only the first, so the second came out
    `'$.farmbarncolor'`, with every separator missing.

    The pieces are recovered the same way as before: render the four canonical
    paths, strip whatever is common to all four -- that is the call around
    them -- and subtract the shorter rendering from the longer.
    """
    cls = getattr(exp, cls_name, None)
    if cls is None:
        return {}

    def render(parts):
        node = cls(this=exp.column("ZZTHISZZ"), expression=exp.JSONPath(expressions=parts))
        return node.sql(dialect=dialect or None)

    root = [exp.JSONPathRoot()]
    try:
        texts = [
            render(root),
            render(root + [exp.JSONPathKey(this="KEY")]),
            render(root + [exp.JSONPathSubscript(this=7)]),
            render(root + [exp.JSONPathKey(this="a b")]),
            render(root + [exp.JSONPathKey(this="KEY"), exp.JSONPathKey(this="LATER")]),
        ]
    except Exception:  # noqa: BLE001 -- this dialect will not write that class
        return {}
    if any("ZZTHISZZ" not in t for t in texts):
        return {}
    # Whatever surrounds the path in all four is the CALL, not the path.
    head = texts[0]
    for t in texts[1:]:
        while not t.startswith(head):
            head = head[:-1]
    tail = texts[0][len(head):]
    for t in texts[1:]:
        while not t.endswith(tail):
            tail = tail[1:]
    inner = [t[len(head): len(t) - len(tail)] for t in texts]
    only_root, with_key, with_sub, with_quoted, with_two = inner

    def suffix(longer):
        return longer[len(only_root):] if longer.startswith(only_root) else longer

    pieces = {
        "Open": only_root,
        "Close": "",
        "Key": suffix(with_key).replace("KEY", "{key}"),
        "Subscript": suffix(with_sub).replace("7", "{index}"),
        "QuotedKey": suffix(with_quoted).replace("a b", "{key}"),
    }
    # The separator before the FIRST key is not always the one before the
    # rest. Databricks writes `c1:item.price` -- a colon, then a dot -- so a
    # form probed from one key alone wrote `c1:itemprice`, with every
    # separator after the first missing.
    pieces["KeyAfter"] = (
        with_two[len(with_key):].replace("LATER", "{key}")
        if with_two.startswith(with_key)
        else pieces["Key"]
    )
    # The pieces have to REBUILD what the reference wrote, or they are not
    # pieces of anything. T-SQL writes the path TWICE -- ISNULL(JSON_QUERY(x,
    # p), JSON_VALUE(x, p)) -- and PostgreSQL writes it variadic, so
    # subtracting one rendering from another leaves fragments of the call
    # mixed into the separators. Those dialects decline here and keep the
    # standalone pieces, which are right for them.
    def rebuild(parts):
        out = pieces["Open"]
        for n, (kind, value) in enumerate(parts):
            form_key = "Key" if n == 0 else "KeyAfter"
            if kind == "sub":
                out += pieces["Subscript"].replace("{index}", str(value))
            elif " " in value:
                out += pieces["QuotedKey"].replace("{key}", value)
            else:
                out += pieces[form_key].replace("{key}", value)
        return out + pieces["Close"]

    wanted = [[], [("key", "KEY")], [("sub", 7)], [("key", "a b")],
              [("key", "KEY"), ("key", "LATER")]]
    if any(rebuild(w) != got for w, got in zip(wanted, inner)):
        return {}
    # The CALL around the path comes from the same measurement, or the two
    # disagree about who writes the separator: the form was probed against the
    # standalone pieces and had absorbed the `$.`, so with corrected pieces the
    # dot was written twice -- `'$..farm.barn.color'`.
    pieces["Form"] = head.replace("ZZTHISZZ", "{this}") + "{path}" + tail
    # And the form for an operand that is NOT a path. The reference writes
    # `GET_JSON_OBJECT(col, path_col)` with the column bare, where the path
    # form would have quoted it into `'$path_col'` -- a different argument.
    # Whether a QUOTE inside a key must be doubled depends on whether the path
    # sits inside a STRING. It cannot be probed by writing one and looking:
    # the reference writes `c -> '$."a'b"'` with the quote bare, which does not
    # tokenize -- the same class of upstream bug as issue (8). So the answer
    # comes from the FORM. An odd number of quotes before the path means the
    # path is inside one.
    #
    # DuckDB writes the path inside `'...'` and needs the escape; Databricks'
    # colon form is bare SQL and must NOT have it, or `col:["fr'uit"]` comes
    # out `col:["fr\'uit"]`.
    pieces["EscapesQuote"] = pieces["Form"].split("{path}")[0].count("'") % 2 == 1
    pieces["PlainForm"] = ""
    try:
        plain = cls(this=exp.column("ZZTHISZZ"), expression=exp.column("ZZPATHZZ")).sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return pieces
    if plain.count("ZZTHISZZ") == 1 and plain.count("ZZPATHZZ") == 1:
        pieces["PlainForm"] = plain.replace("ZZTHISZZ", "{this}").replace(
            "ZZPATHZZ", "{path}"
        )
    return pieces


def composite_type_sql(dialect: str) -> dict[str, str]:
    """How a dialect writes a type that CONTAINS another type.

    Three shapes, and the port needs all three because the parser now builds
    them: an array of T, a struct of named fields, and a map of two types.
    They do not share a spelling -- DuckDB writes `INT[]` and `STRUCT(a INT)`,
    Databricks writes `ARRAY<INT>` and `STRUCT<a: INT>` -- and the array form
    is not even the same SHAPE as the other two, so it is read back as a
    template rather than as a pair of delimiters.

    Probed by rendering a known nested type and reading the result apart. The
    inner type is written by the dialect too (T-SQL says INTEGER, not INT), so
    the template is cut on the inner rendering rather than on the literal
    text that went in. PROBED.
    """
    import sqlglot

    d = dialect or None

    def to(sql: str) -> str:
        # Parsed as DuckDB, which spells every one of these forms, and written
        # in the dialect under test.
        written = sqlglot.parse_one(sql, read="duckdb").sql(dialect=d)
        return written[written.index(" AS ") + 4 : -1]

    inner = to("CAST(x AS INT)")
    array = to("CAST(x AS INT[])")
    if inner not in array:
        raise SystemExit(f"{dialect}: array type {array!r} does not contain {inner!r}")
    result = {"ArrayTemplate": array.replace(inner, "{inner}", 1)}

    # A fixed-size array is its OWN template, not the plain one with a suffix:
    # DuckDB puts the size inside the brackets it already wrote (`INT[3]`),
    # Databricks appends a second pair after the angle brackets
    # (`ARRAY<INT>[3]`). Reading it as a suffix of the plain form got DuckDB
    # wrong, so both are recorded whole.
    sized = to("CAST(x AS INT[3])")
    if inner not in sized or "3" not in sized:
        raise SystemExit(f"{dialect}: sized array {sized!r} lacks the type or the size")
    result["ArraySizedTemplate"] = sized.replace(inner, "{inner}", 1).replace("3", "{size}", 1)

    struct = to("CAST(x AS STRUCT(a INT))")
    head, rest = struct.split("STRUCT", 1)
    if head:
        raise SystemExit(f"{dialect}: struct type {struct!r} does not start with STRUCT")
    field = rest[1:-1]
    if not field.endswith(inner):
        raise SystemExit(f"{dialect}: struct field {field!r} does not end with {inner!r}")
    result["StructOpen"] = rest[0]
    result["StructClose"] = rest[-1]
    result["StructFieldSep"] = field[len("a") : -len(inner)]

    # MAP reuses the struct delimiters everywhere so far; assert rather than
    # record a second pair that has never differed.
    m = to("CAST(x AS MAP(TEXT, INT))")
    if not (m.startswith("MAP" + result["StructOpen"]) and m.endswith(result["StructClose"])):
        raise SystemExit(f"{dialect}: map type {m!r} does not use the struct delimiters")
    return result


def quantifier_wraps_subquery(dialect: str) -> dict[str, bool]:
    """Whether a quantifier over a QUERY keeps the Subquery wrapper.

    It does for ANY and does not for ALL -- `= ANY (SELECT 1)` arrives as
    Any(Subquery(Select)) and `= ALL (SELECT 1)` as All(Select). The port had
    this recorded as a DIALECT disagreement and refused both; it is neither a
    dialect fact nor an operator fact but a per-CLASS one, so it is probed per
    class here and comes back the same in every dialect. PROBED.
    """
    import sqlglot

    out = {}
    for word, cls in (("ANY", "Any"), ("ALL", "All")):
        e = sqlglot.parse_one(
            f"SELECT x FROM t WHERE a = {word} (SELECT 1)", read=dialect or None
        )
        node = e.args["where"].this.args["expression"]
        if type(node).__name__ != cls:
            raise SystemExit(f"{dialect}: {word} over a query is {type(node).__name__}")
        out[cls] = type(node.this).__name__ == "Subquery"
    return out


def quantifier_query_sql(dialect: str) -> dict[str, str]:
    """How a quantifier over a QUERY is written, as a template.

    Not the same spacing as over an array, and not the same between the two
    classes: ANY takes the Subquery's own parentheses (`ANY (SELECT 1)`) while
    ALL carries a bare Select and supplies its own. Probed by rendering both
    and cutting on the query, rather than by re-deriving the reference's
    wrap() rules here. PROBED.
    """
    import sqlglot

    from sqlglot import exp

    out = {}
    for word, cls in (("ANY", "Any"), ("ALL", "All")):
        e = sqlglot.parse_one(
            f"SELECT x FROM t WHERE a = {word} (SELECT 1)", read=dialect or None
        )
        node = e.args["where"].this.args["expression"]

        def render(n):
            written = e.sql(dialect=dialect or None)
            tail = written[written.index("a = ") + 4 :]
            if not tail.startswith(word) or "SELECT 1" not in tail:
                raise SystemExit(f"{dialect}: {word} over a query rendered {tail!r}")
            return tail.replace("SELECT 1", "{query}", 1)

        out[cls] = render(node)
        # The SAME node written two ways. A quantifier over a Subquery keeps
        # the parentheses the subquery already carries; over a BARE query the
        # reference supplies its own and drops the space -- `ANY(SELECT 1)`
        # against `ANY (SELECT 1)`. Probing only the first spelling produced a
        # generator that was right until simplify handed it the second.
        inner = node.this
        if isinstance(inner, exp.Subquery):
            node.set("this", inner.this)
            out[cls + "Unwrapped"] = render(node)
            node.set("this", inner)
        else:
            out[cls + "Unwrapped"] = out[cls]
    return out


def placeholder_sql(dialect: str) -> dict[str, str]:
    """How a bound parameter is written. Not one spelling: DuckDB writes
    `$name`, PostgreSQL `%(name)s`, everyone else `:name`, and the anonymous
    form is `?` except in PostgreSQL, where it is `%s`. PROBED.
    """
    import sqlglot

    d = dialect or None
    named = sqlglot.parse_one(":zzname", read="databricks").sql(dialect=d)
    if "zzname" not in named:
        raise SystemExit(f"{dialect}: named placeholder rendered {named!r}")
    parameter = sqlglot.parse_one("@zzname", read="tsql").sql(dialect=d)
    if "zzname" not in parameter:
        raise SystemExit(f"{dialect}: parameter rendered {parameter!r}")
    parameter = parameter.replace("zzname", "{name}", 1)
    return {
        "Named": named.replace("zzname", "{name}", 1),
        "Anonymous": sqlglot.parse_one("?", read="databricks").sql(dialect=d),
        # A placeholder carrying `jdbc` writes back as `?` even in PostgreSQL,
        # where the plain one is `%s`. Same node, two spellings.
        "AnonymousJDBCSQL": sqlglot.parse_one("?", read="postgres").sql(dialect=d),
        # A Parameter is a different node from a Placeholder and has its own
        # spelling: `@x` in T-SQL, `$x` in DuckDB and PostgreSQL, `${x}` in
        # Databricks. DuckDB writes BOTH nodes as `$x`, which is the
        # reference's own ambiguity and not one to resolve here.
        "Parameter": parameter,
        # What each SPELLING means here, which is not the same anywhere. `$nm`
        # is a Placeholder in DuckDB, a Parameter in PostgreSQL and Databricks
        # and a plain column elsewhere; `@nm` is a Parameter everywhere except
        # DuckDB, where `@` is ABSOLUTE VALUE. Reading `@nm` as a Parameter
        # there mismatched three statements against the reference. The port
        # writes all of these, so it has to be able to read them back, and it
        # cannot do that from one rule.
        "DollarName": _form_class(":", "$nm", d),
        "DollarNumber": _form_class(":", "$1", d),
        "AtName": _form_class(":", "@nm", d),
        "PercentNamed": _form_class(":", "%(nm)s", d),
        "PercentAnonymous": _form_class(":", "%s", d),
        # PostgreSQL stamps `jdbc` on the anonymous form and nobody else does.
        "AnonymousJDBC": bool(
            (sqlglot.parse_one("?", read=d).args or {}).get("jdbc")
        ),
    }


def _form_class(_unused: str, sql: str, dialect) -> str:
    """The node class this dialect reads `sql` as, or "" if it cannot."""
    import sqlglot

    try:
        return type(sqlglot.parse_one(sql, read=dialect)).__name__
    except Exception:  # noqa: BLE001 -- a spelling this dialect does not have
        return ""


def binary_range_ops(dialect: str, P, tokenizer_keywords) -> dict:
    """The range operators that are just a binary node, by token.

    PostgreSQL alone has a dozen -- `@>`, `<@`, `&&`, `-|-`, `?&`, `~*` --
    and every one of them builds the same shape: a class with `this` and
    `expression`. The port had five of them hand-listed and refused the rest,
    which cost 37 statements.

    So they are probed: run `a <op> b` through the reference and keep the
    class where the result really is a plain two-argument binary. Anything
    with a shape of its own -- IS, IN, BETWEEN, and LIKE's ESCAPE -- fails
    that test and stays with the rules written for it. PROBED.
    """
    import sqlglot

    out = {}
    for token in P.RANGE_PARSERS:
        for text in tokenizer_keywords.get(token, ()):
            try:
                node = sqlglot.parse_one(f"a {text} b", read=dialect or None)
            except Exception:  # noqa: BLE001 -- not a spelling this dialect has
                continue
            if sorted(node.args) != ["expression", "this"]:
                continue
            left, right = node.args["this"], node.args["expression"]
            if getattr(left, "name", None) != "a" or getattr(right, "name", None) != "b":
                continue
            # The class AND how this dialect writes it back. The two are not
            # the same fact: `~*` reads as RegexpILike here and PostgreSQL
            # writes it `~*` again, but another dialect could spell the same
            # node differently.
            written = node.sql(dialect=dialect or None)
            head, sep, tail = written.partition("a ")
            op = tail.rpartition(" b")[0] if sep else ""
            out[token.name] = {"class": type(node).__name__, "op": op}
            break
    return out


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
    for cls_name in ("All", "Any"):
        for key, operand in (
            (cls_name, exp.Paren(this=exp.column("ZZOPERANDZZ"))),
            # An operand that brings no parentheses of its own is spaced
            # differently: `ANY(x)` around a Paren but `ANY ('a', 'b')` around
            # a Tuple. Probed with a Paren alone, the spelling was right for
            # the one operand the port could build and wrong for the row it
            # can build now.
            (cls_name + "Value", exp.column("ZZOPERANDZZ")),
        ):
            node = getattr(exp, cls_name)(this=operand.copy())
            try:
                text = node.sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001
                continue
            rendered = operand.sql(dialect=dialect or None)
            if not text.endswith(rendered):
                continue
            out[key] = text[: -len(rendered)]
    return out


def within_group_absorbed_by(dialect: str) -> list:
    """Which CLASSES absorb a following WITHIN GROUP instead of being wrapped.

    `STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y)` is one GroupConcat carrying
    the order -- in every dialect -- while `PERCENTILE_CONT(0.5) WITHIN GROUP
    (...)` is a WithinGroup wrapping the call. So the fold belongs to the
    FUNCTION, not to the dialect; asking the question per dialect said "always"
    and refused twenty statements that wrap perfectly well.
    """
    import sqlglot

    absorbed = []
    for sql in (
        "SELECT STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y)",
        "SELECT LISTAGG(x) WITHIN GROUP (ORDER BY y)",
        "SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY y)",
        "SELECT PERCENTILE_DISC(0.5) WITHIN GROUP (ORDER BY y)",
    ):
        try:
            e = sqlglot.parse_one(sql, dialect=dialect or None).selects[0]
        except Exception:  # noqa: BLE001
            continue
        if type(e).__name__ != "WithinGroup":
            absorbed.append(type(e).__name__)
    return sorted(set(absorbed))


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


def _cast_probe(builder, plain, base_sql, i, probe, dialect, name, sensitive, keyof=None,
                vanished=None):
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
        # The argument VANISHED. DuckDB drops a zero group from REGEXP_EXTRACT
        # entirely, and that is a rule the port can FOLLOW rather than refuse:
        # recorded apart, and only when the rest of the call is untouched.
        if vanished is not None and got == base_sql.replace(", " + token, "", 1):
            _record(vanished, name, len(plain), i, keyof)
            return
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
    # Where a literal ZERO simply DISAPPEARS and the rest of the call is
    # untouched. That is a rule to follow, not a reason to refuse -- and
    # refusing it also turned away every OTHER literal in the same slot,
    # which renders perfectly.
    drops_zero: dict[str, dict[int, list[str]]] = {}
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
                    dialect, cls_name, zero_sensitive, keyof, drops_zero,
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

    return tidy(sensitive), tidy(zero_sensitive), tidy(drops_zero)


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
                if isinstance(value, list):
                    # A list-valued arg needs a LIST placeholder. Passing a
                    # bare column made the reference raise, so no template was
                    # recorded for any class with one -- Unnest among them.
                    expr_keys.append(key + "[]")
                elif isinstance(value, exp.Expr):
                    expr_keys.append(key)
                elif isinstance(value, (str, bool)):
                    # A FLAG is a shape too. `WITH UNIQUE KEYS` is carried as
                    # one, and with bools skipped here no template was ever
                    # probed for it -- so the template that writes only the
                    # pairs matched and silently dropped the clause.
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


def _render(exp, node, dialect):
    """This node as its PARENT would receive it, outer whitespace kept."""
    from sqlglot.dialects.dialect import Dialect

    return Dialect.get_or_raise(dialect or None).generator().sql(node)


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
        # Sorted by their TEXT: a variant may hold a bool beside a string now
        # that flags are shapes, and the two do not compare.
        for expr_keys, scalars in sorted(variants, key=repr):
            # An ALREADY-UPPERCASE placeholder. A unit comes back upper-cased --
            # T-SQL renders DATEADD(__AUNIT__, ...) from a `unit` argument --
            # and a lowercase marker simply is not there to replace, so every
            # call form of DateAdd was rejected for want of a token.
            kwargs = {}
            for k in expr_keys:
                if k.endswith("[]"):
                    kwargs[k[:-2]] = [exp.column(f"ZZ{k[:-2].upper()}ZZ")]
                else:
                    kwargs[k] = exp.column(f"ZZ{k.upper()}ZZ")
            kwargs.update(dict(scalars))
            try:
                # Rendered through the GENERATOR rather than the node, which
                # strips the outer whitespace. A child hands its parent
                # whatever its writer returned, leading space and all --
                # LimitOptions returns " ROWS ONLY" -- and a template that
                # substitutes the stripped form writes `FETCH FIRST 1ROWS ONLY`.
                text = _render(exp, cls(**kwargs), dialect)
            except Exception:  # noqa: BLE001 -- this dialect will not write that shape
                continue
            ok = True
            for key in [k[:-2] if k.endswith("[]") else k for k in expr_keys]:
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
                if isinstance(value, str) and text.count(value) == 1:
                    text = text.replace(value, "{" + key + "}")
                else:
                    # A flag never appears as TEXT -- the words it selects do
                    # -- so it is always a condition on the spelling.
                    required.append((key, value))
            marked = [k[:-2] if k.endswith("[]") else k for k in expr_keys] + [
                k for k, _ in scalars if (k, dict(scalars)[k]) not in required
            ]
            # Infix templates are rejected. `a #> b` needs parentheses around a
            # child by PRECEDENCE, and a template substitutes text without
            # knowing any -- the reference writes `a #> (n IN (1, 2))` and a
            # template would write it flat. A template that begins with an
            # argument is infix; the classes that need one already have a
            # writer that knows the precedence table.
            # A template that BEGINS with a marker and continues with words --
            # `{this} WITHIN GROUP (...)` -- is a postfix modifier, not an
            # infix operator: there is no right-hand operand whose precedence
            # could matter. Only a template that is marker-operator-marker is
            # rejected here; the writer guards the left operand instead.
            stripped = text.lstrip()
            if stripped.startswith("{"):
                rest = stripped[stripped.find("}") + 1 :].strip()
                if not rest or not rest[0].isalpha():
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
            # A FALSE flag is not a key the node carries -- the writer counts
            # a bool as present only when it is set, so listing it here made
            # the key counts disagree and cost 22 statements their template.
            keys = [k[:-2] if k.endswith("[]") else k for k in expr_keys] + [
                k for k, v in scalars if v is not False
            ]
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
    for name, cls_name, spec in funcs:
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
        # A constant the builder supplies as a NODE rather than a scalar.
        # SHA384 and SHA256 are one class told apart by length, and feeding
        # the probe a node it could not see meant one spelling was recorded
        # for the whole class: SHA384(x) came back written SHA256(x). What is
        # compared is the constant's own RENDERING, since a Literal holds
        # "384" and a Boolean holds a bool.
        nodes = []
        for k, how in spec:
            if "node" not in how:
                continue
            try:
                built = getattr(exp, how["node"])(**dict(how.get("extras") or []))
                nodes.append((k, built, built.sql(dialect=dialect or None)))
            except Exception:  # noqa: BLE001 -- not a constant this dialect writes
                nodes = None
                break
        if nodes is None:
            continue
        kwargs.update(dict(consts))
        kwargs.update({k: n for k, n, _ in nodes})
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
            narrowed.update({k: n for k, n, _ in nodes})
            for i, (key, how) in enumerate(positional[:width]):
                narrowed[key] = [args[i]] if "varlen" in how else args[i]
            try:
                rendered = cls(**narrowed).sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001
                continue
            used = keys[:width]
            absent = (
                consts
                + [(k, text) for k, _, text in nodes]
                + [(k, None) for k in keys[width:]]
            )
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

    # A spelling says nothing about a key its own form never fills, and with
    # every arity offered that is a licence to DROP an argument: PostgreSQL's
    # one-argument WIDTH_BUCKET matched a node carrying two and wrote
    # WIDTH_BUCKET(10) for WIDTH_BUCKET(10, ARRAY[5, 15]). Each spelling is
    # held to every key any spelling of the class mentions, so a key it does
    # not write has to be absent for it to apply.
    for cls_name, candidates in out.items():
        known = {k for _, keys, consts, _ in candidates for k in list(keys) + [c[0] for c in consts]}
        for i, (keyword, keys, consts, no_parens) in enumerate(candidates):
            named = set(keys) | {c[0] for c in consts}
            extra = [(k, None) for k in sorted(known - named)]
            candidates[i] = (keyword, keys, consts + extra, no_parens)
        seen, unique = set(), []
        for cand in candidates:
            mark = repr(cand)
            if mark not in seen:
                seen.add(mark)
                unique.append(cand)
        out[cls_name] = unique

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
        if "node" in how:
            # Index -1 marks a CONSTANT node rather than a wrapper: the class
            # goes in the same slot, and the index tells the two apart.
            extras = "".join(
                "{%s, %s}, " % (gostr(k), goconst(v)) for k, v in (how.get("extras") or [])
            )
            parts.append(
                f"{{{gostr(key)}, -1, false, nil, {gostr(how['node'])}, "
                f"[]FuncConst{{{extras}}}}}"
            )
        elif "wrap" in how:
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
        "\t// DropsZeroArgs are the argument keys a literal ZERO simply\n",
        "\t// DISAPPEARS from -- DuckDB writes REGEXP_EXTRACT(x, p) for a\n",
        "\t// zero group. A rule to FOLLOW, unlike the two below, and kept\n",
        "\t// apart from them because refusing it also turned away every\n",
        "\t// other literal in the same slot.\n",
        "\tDropsZeroArgs map[string]map[int][]string\n",
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
        "\t// JSONArrowTypesWithoutPath says whether only_json_types is still\n",
        "\t// set when the operand is NOT a path -- PostgreSQL's builder\n",
        "\t// returns before stamping it, DuckDB's stamps it regardless.\n",
        "\tJSONArrowTypesWithoutPath bool\n",
        "\t// JSONExtractNeedsParens lists the extraction classes this dialect\n",
        "\t// writes as an OPERATOR, which the reference parenthesises when one\n",
        "\t// is an operand: `(a -> b) & c`, but Databricks' `a:b & c` plain.\n",
        "\tJSONExtractNeedsParens map[string]bool\n",
        "\t// VariantExtractColon says `x:a` is a JSON extraction here, the\n",
        "\t// form Databricks writes and this port used to write without ever\n",
        "\t// being able to read one back.\n",
        "\tVariantExtractColon bool\n",
        "\t// JSONKeyValueSQL is one key/value pair inside JSON_OBJECT: DuckDB\n",
        "\t// separates them with a comma and the others with a colon.\n",
        "\tJSONKeyValueSQL string\n",
        "\t// DefaultSampleMethod is the TABLESAMPLE method this dialect records\n",
        "\t// when the statement names none. DuckDB says RESERVOIR either way.\n",
        "\tDefaultSampleMethod string\n",
        "\t// BareSampleCountIsPercent: `TABLESAMPLE (3)` is a PERCENTAGE in\n",
        "\t// PostgreSQL and a number of rows everywhere else.\n",
        "\tBareSampleCountIsPercent bool\n",
        "\t// PrefixAlias: `SELECT foo: 1` NAMES the expression, DuckDB's\n",
        "\t// spelling of `SELECT 1 AS foo`. The same characters are a JSON\n",
        "\t// extraction in Databricks, so the dialect decides, not the shape.\n",
        "\tPrefixAlias bool\n",
        "\t// The four conventions a PIVOT node carries that the statement never\n",
        "\t// says: how output columns are named, and three flags stamped on.\n",
        "\t// VersionRangeSep is the word between the two bounds of a FOR\n",
        "\t// SYSTEM_TIME range, which are held as a Tuple: `c TO d`, `c AND d`.\n",
        "\tVersionRangeSep map[string]string\n",
        "\t// ForClauseOptions is the option vocabulary of FOR XML and FOR JSON,\n",
        "\t// by kind: each word, and the second word it may take after it.\n",
        "\tForClauseOptions map[string]map[string][]string\n",
        "\t// TableSampleWord and SelectSampleWord are what a sample is called\n",
        "\t// after a TABLE and after the QUERY: DuckDB says TABLESAMPLE for the\n",
        "\t// one and USING SAMPLE for the other, for the very same node.\n",
        "\t// GroupConcatOrder says where a folded ORDER BY goes when a\n",
        "\t// GroupConcat is written back: inside the first argument, or\n",
        "\t// unfolded into a WITHIN GROUP. Empty means neither, and refuse.\n",
        "\t// WithinGroupFolds are the function NAMES whose builder swallows a\n",
        "\t// following WITHIN GROUP instead of being wrapped by one. It is the\n",
        "\t// NAME that decides, not the class: Databricks folds STRING_AGG and\n",
        "\t// not LISTAGG, and both build a GroupConcat.\n",
        "\tWithinGroupFolds map[string]struct{}\n",
        "\t// MapBraceLiteral: `MAP {k: v}` is a map LITERAL here. Elsewhere MAP\n",
        "\t// is an ordinary name and the braces are a struct.\n",
        "\tMapBraceLiteral bool\n",
        "\t// RewritesCreateAsSelect: this dialect has no CREATE TABLE AS SELECT\n",
        "\t// and the reference turns it into something else, which is a\n",
        "\t// transformation rather than a spelling.\n",
        "\tRewritesCreateAsSelect bool\n",
        "\tGroupConcatOrder string\n",
        "\tTableSampleWord  string\n",
        "\tSelectSampleWord string\n",
        "\tPivotColumnNaming        string\n",
        "\tPivotIdentifiesStrings   bool\n",
        "\tPivotPrefixesColumns     bool\n",
        "\tUnpivotValueColumnsFirst bool\n",
        "\t// JSONPathFunctions are the names that turn their arguments into a\n",
        "\t// JSON PATH rather than holding them. Probed with STRING literals,\n",
        "\t// because with the placeholder columns the generic probe uses they\n",
        "\t// all fall back to a plain positional shape that real SQL never takes.\n",
        "\tJSONPathFunctions map[string]JSONPathFunc\n",
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
        "\t// JSONPathByClass overrides those pieces for a class that writes a\n",
        "\t// path differently from the standalone rendering. Only the SEPARATORS\n",
        "\t// are overridden; the wrapper comes from the call\'s own spelling.\n",
        "\tJSONPathByClass map[string]JSONPathSQL\n",
        "\t// JSONExtractSQL is how the operator wraps a path, where the\n",
        "\t// dialect writes it as one. A dialect that explodes the path into\n",
        "\t// arguments has no entry and is refused.\n",
        "\tJSONExtractSQL map[string]string\n",
        "\t// JSONPerPartSQL is for dialects with no single path literal:\n",
        "\t// PostgreSQL writes one operator per part, or one ARGUMENT per\n",
        "\t// part when the node is not restricted to JSON types. One node\n",
        "\t// becomes N of something, which is a transform, not a spelling.\n",
        "\tJSONPerPartSQL map[string]JSONPerPart\n",


        "\tBracketIsRewritten bool\n",
        "\t// IndexOffset is how far this dialect's subscripts are from\n",
        "\t// sqlglot's 0-based Bracket: 1 in DuckDB and PostgreSQL, 0\n",
        "\t// elsewhere. The parser subtracts it from a written index.\n",
        "\tIndexOffset int\n",
        "\t// SafeToEliminateDoubleNegation gates the optimizer's NOT NOT x\n",
        "\t// -> x rule. True in every dialect this port configures, which is\n",
        "\t// exactly why it is read rather than assumed: a rule that is right\n",
        "\t// everywhere today is not the same as a rule with no condition.\n",
        "\tSafeToEliminateDoubleNegation bool\n",
        "\t// Placeholder is how a bound parameter is written: `$name` in\n",
        "\t// DuckDB, `%(name)s` in PostgreSQL, `:name` elsewhere.\n",
        "\tPlaceholder PlaceholderSQL\n",
        "\t// QuantifierWrapsSubquery: whether a quantifier over a QUERY\n",
        "\t// keeps the Subquery wrapper. ANY does and ALL does not, in\n",
        "\t// every dialect -- a per-class fact, not a per-dialect one.\n",
        "\tQuantifierWrapsSubquery map[string]bool\n",
        "\t// QuantifierQuerySQL is the same over a QUERY, where the\n",
        "\t// spacing and the parentheses both differ from the array form.\n",
        "\tQuantifierQuerySQL map[string]string\n",
        "\t// NestedTypeKinds are the DataType kinds the reference marks\n",
        "\t// `nested`, whether or not they were written with parameters.\n",
        "\t// StructTypeKinds is the subset whose parameters are NAMED\n",
        "\t// fields (ColumnDef) rather than bare types.\n",
        "\tNestedTypeKinds map[string]bool\n",
        "\tStructTypeKinds map[string]bool\n",
        "\t// SupportsFixedSizeArrays: whether `INT[3]` is a type at all.\n",
        "\t// Where it is not, the reference RETREATS and reads the\n",
        "\t// brackets as a subscript of the cast instead.\n",
        "\tSupportsFixedSizeArrays bool\n",
        "\tCompositeType           CompositeTypeSQL\n",
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
        "\t// WithinGroupAbsorbedBy are the classes that FOLD a following\n",
        "\t// WITHIN GROUP into themselves instead of being wrapped by it.\n",
        "\tWithinGroupAbsorbedBy map[string]bool\n",
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
        "\t// TypeDispatchFunctions are the names whose CLASS depends on the\n",
        "\t// TYPE of one argument. DuckDB's DATE_TRUNC builds a DateTrunc\n",
        "\t// over a DATE and a TimestampTrunc over anything else -- two\n",
        "\t// different shapes, not just two names, which is why each carries\n",
        "\t// a whole spec. Answerable only with a type annotator, which is\n",
        "\t// why these were refused until there was one.\n",
        "\tTypeDispatchFunctions map[string]TypeDispatch\n",
        "\t// ValidIntervalUnits are the words that can follow INTERVAL as a\n",
        "\t// TYPE. Where the next word is not one, the reference builds a\n",
        "\t// bare INTERVAL type instead of reading a unit that is not there.\n",
        "\tValidIntervalUnits map[string]struct{}\n",
        "\t// BinaryRangeOps are the range operators that are just a binary\n",
        "\t// node -- PostgreSQL's `@>`, `&&`, `-|-` and the rest. Probed by\n",
        "\t// running `a <op> b` and keeping the class where the result is\n",
        "\t// really a plain two-argument binary; IS, IN and BETWEEN have\n",
        "\t// shapes of their own and are not here.\n",
        "\tBinaryRangeOps map[TokenType]string\n",
        "\t// BinaryRangeSQL is how each of those is written back.\n",
        "\tBinaryRangeSQL map[string]string\n",
        "\t// TypeTokens maps a type keyword to the DataType.Type member the\n",
        "\t// reference records. A few type tokens have no member and are absent,\n",
        "\t// which refuses them rather than inventing one.\n",
        "\tTypeTokens map[TokenType]string\n",
        "}\n\n",
        "// FuncSpec is how one function name becomes a node: the class, and what\n",
        "// fills each of its argument keys.\n",
        "// TypeDispatch is a name that builds a different node depending on\n",
        "// the type of one of its arguments.\n",
        "type TypeDispatch struct {\n",
        "\tIndex   int\n",
        "\tDefault FuncSpec\n",
        "\tByType  map[string]FuncSpec\n",
        "}\n\n",
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
        "// JSONPerPart is how one path part is written, folded left over the\n",
        "// parts: Chain for the operator form, Call for the function form.\n",
        "type JSONPerPart struct {\n",
        "\tChain string\n",
        "\tCall  string\n",
        "}\n",
        "\n",
        "// CompositeTypeSQL is how a type that contains another type is\n",
        "// written. The array form is a TEMPLATE rather than a pair of\n",
        "// delimiters because it is not the same shape everywhere: DuckDB\n",
        "// suffixes (`INT[]`) and Databricks wraps (`ARRAY<INT>`).\n",
        "type CompositeTypeSQL struct {\n",
        "\tArrayTemplate      string\n",
        "\tArraySizedTemplate string\n",
        "\tStructOpen         string\n",
        "\tStructClose        string\n",
        "\tStructFieldSep     string\n",
        "}\n",
        "\n",
        "// PlaceholderSQL is the text around a bound parameter.\n",
        "type PlaceholderSQL struct {\n",
        "\tNamed     string\n",
        "\tAnonymous string\n",
        "\tParameter string\n",
        "\t// The node CLASS each spelling means in this dialect, empty\n",
        "\t// where the dialect has no such spelling. `@nm` is a Parameter\n",
        "\t// everywhere but DuckDB, where `@` is absolute value.\n",
        "\tDollarName       string\n",
        "\tDollarNumber     string\n",
        "\tAtName           string\n",
        "\tPercentNamed     string\n",
        "\tPercentAnonymous string\n",
        "\t// AnonymousJDBC: PostgreSQL stamps `jdbc` on a bare `?`, and\n",
        "\t// writes that node back as `?` where a plain one is `%s`.\n",
        "\tAnonymousJDBC    bool\n",
        "\tAnonymousJDBCSQL string\n",
        "}\n",
        "\n",
        "// JSONPathSQL is the text around each piece of a JSON path.\n",
        "type JSONPathSQL struct {\n",
        "\t// Form is the call around the path, for an override -- measured with\n",
        "\t// the same rendering as the separators so the two agree on who\n",
        "\t// writes the dot after the root.\n",
        "\tForm string\n",
        "\t// PlainForm is that same call with an operand that is NOT a path,\n",
        "\t// which the path form would have quoted into something else.\n",
        "\tPlainForm string\n",
        "\tOpen      string\n",
        "\tClose     string\n",
        "\tKey       string\n",
        "\t// KeyAfter is the form for a key that is not the FIRST: Databricks\n",
        "\t// writes `c1:item.price`, a colon and then a dot.\n",
        "\tKeyAfter  string\n",
        "\t// EscapesQuote says a quote inside a key is doubled, which it must\n",
        "\t// be where the path is written inside a string and must NOT be\n",
        "\t// where the form is bare SQL.\n",
        "\tEscapesQuote bool\n",
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
        funcs, by_arity, unit_maps, dispatch = probe_functions(P, exp, name)
        if dispatch:
            out.append("\t\tTypeDispatchFunctions: map[string]TypeDispatch{\n")
            for fn in sorted(dispatch):
                d = dispatch[fn]
                dcls, dspec = d["default"]
                out.append(f"\t\t\t{gostr(fn)}: {{\n")
                out.append(f"\t\t\t\tIndex: {d['index']},\n")
                out.append(
                    f"\t\t\t\tDefault: FuncSpec{{{gostr(dcls)}, "
                    f"[]FuncArg{{{funcargs(dspec)}}}}},\n"
                )
                out.append("\t\t\t\tByType: map[string]FuncSpec{\n")
                for ty in sorted(d["by_type"]):
                    cls, spec = d["by_type"][ty]
                    out.append(
                        f"\t\t\t\t\t{gostr(ty)}: {{{gostr(cls)}, "
                        f"[]FuncArg{{{funcargs(spec)}}}}},\n"
                    )
                out.append("\t\t\t\t},\n\t\t\t},\n")
            out.append("\t\t},\n")
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
        # The generator needs a spelling for these too. Every arity is
        # offered, not just the widest: a narrow form is where a builder
        # supplies a constant, and with only the widest recorded the writer
        # knew just the spelling that writes that constant out by hand --
        # REGEXP_EXTRACT(a, b) came out as REGEXP_EXTRACT(a, b, 1).
        render_input = dict(funcs)
        render_forms = [(fname, cls, spec) for fname, (cls, spec) in funcs.items()]
        for fname, variants in by_arity.items():
            render_input.setdefault(fname, variants[max(variants)])
            for arity in sorted(variants, reverse=True):
                cls, spec = variants[arity]
                render_forms.append((fname, cls, spec))
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
                        "".join("{%s, %s}, " % (gostr(k), goconst(v)) for k, v in required),
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
        casts, zeros, drops = cast_sensitive_args(P, exp, name, render_input)
        for field, table in (("ZeroSensitiveArgs", zeros), ("CastSensitiveArgs", casts),
                             ("DropsZeroArgs", drops)):
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
        out.append(sqlmap("FunctionSQL", render_functions(P, exp, name, render_forms)))
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
        _oj, _so, _ojp = json_arrow_flags(name)
        out.append(f"\t\tJSONArrowOnlyJSONTypes: {str(_oj).lower()},\n")
        out.append(f"\t\tJSONArrowSetsScalarOnly: {str(_so).lower()},\n")
        out.append(f"\t\tJSONArrowTypesWithoutPath: {str(_ojp).lower()},\n")
        jpf = json_path_functions(P, exp, name)
        if jpf:
            out.append("\t\tJSONPathFunctions: map[string]JSONPathFunc{\n")
            for fn in sorted(jpf):
                e = jpf[fn]
                consts = "".join(
                    "{%s, %s}, " % (gostr(k), goconst(v)) for k, v in e["consts"]
                )
                out.append(
                    "\t\t\t%s: {%s, %s, []FuncConst{%s}, %s, %s, %d, %s},\n"
                    % (
                        gostr(fn),
                        gostr(e["class"]),
                        str(e["fold"]).lower(),
                        consts,
                        str(e["keeps_tail"]).lower(),
                        str(e.get("int_subscripts", False)).lower(),
                        e.get("index_shift", 0),
                        str(e["root_default"]).lower(),
                    )
                )
            out.append("\t\t},\n")
        out.append(
            f"\t\tVariantExtractColon: {str(variant_extract_colon(name)).lower()},\n"
        )
        out.append(f"\t\tPrefixAlias: {str(prefix_alias(name)).lower()},\n")
        _wgf = within_group_folding_names(name, P)
        if _wgf:
            out.append(strset("WithinGroupFolds", _wgf))
        out.append(f"\t\tMapBraceLiteral: {str(map_brace_literal(name)).lower()},\n")
        out.append(
            "\t\tRewritesCreateAsSelect: %s,\n"
            % str(create_as_select_rewritten(name)).lower()
        )
        _gco = group_concat_order(name)
        if _gco:
            out.append(f"\t\tGroupConcatOrder: {gostr(_gco)},\n")
        _ss = select_sample_words(name, exp)
        if _ss[0] and _ss[1] and _ss[0] != _ss[1]:
            out.append(f"\t\tTableSampleWord: {gostr(_ss[0])},\n")
            out.append(f"\t\tSelectSampleWord: {gostr(_ss[1])},\n")
        _fc = for_clause_options(name)
        if _fc:
            out.append("\t\tForClauseOptions: map[string]map[string][]string{\n")
            for kind in sorted(_fc):
                out.append(f"\t\t\t{gostr(kind)}: {{\n")
                for word in sorted(_fc[kind]):
                    follows = ", ".join(gostr(f) for f in _fc[kind][word])
                    out.append(f"\t\t\t\t{gostr(word)}: {{{follows}}},\n")
                out.append("\t\t\t},\n")
            out.append("\t\t},\n")
        _vr = version_range_separators(name, exp)
        if _vr:
            out.append("\t\tVersionRangeSep: map[string]string{\n")
            for kind in sorted(_vr):
                out.append(f"\t\t\t{gostr(kind)}: {gostr(_vr[kind])},\n")
            out.append("\t\t},\n")
        _pc = pivot_conventions(name)
        if _pc["naming"]:
            out.append(f"\t\tPivotColumnNaming: {gostr(_pc['naming'])},\n")
        for _key, _field in (
            ("identify_strings", "PivotIdentifiesStrings"),
            ("prefixed_columns", "PivotPrefixesColumns"),
            ("value_columns_first", "UnpivotValueColumnsFirst"),
        ):
            out.append(f"\t\t{_field}: {str(_pc[_key]).lower()},\n")
        out.append(
            "\t\tBareSampleCountIsPercent: %s,\n"
            % str(bare_sample_count_is_percent(name, exp)).lower()
        )
        _dsm = default_sample_method(name, exp)
        if _dsm:
            out.append(f"\t\tDefaultSampleMethod: {gostr(_dsm)},\n")
        _kv = json_key_value_sql(name, exp)
        if _kv:
            out.append(f"\t\tJSONKeyValueSQL: {gostr(_kv)},\n")
        _parens = json_extract_needs_parens(name)
        if any(_parens.values()):
            out.append("\t\tJSONExtractNeedsParens: map[string]bool{\n")
            for cls in sorted(k for k, v in _parens.items() if v):
                out.append(f"\t\t\t{gostr(cls)}: true,\n")
            out.append("\t\t},\n")
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
        _jc = json_extract_chained(name, exp)
        # Only where the dialect writes one operator or argument PER PART. A
        # dialect that puts the whole path in one operator needs no fold and
        # gets no entry.
        _folded = {
            cls: forms
            for cls, forms in _jc.items()
            if forms.get("Chain") and forms["Chain"][1].count("KEYA") == 1
            and "KEYB" in forms["Chain"][1]
            and forms["Chain"][1] != forms["Chain"][0].replace("{this}", "THIS").replace(
                "{part}", "KEYA"
            )
            and forms["Chain"][1].count(forms["Chain"][0].split("{part}")[0].split("{this}")[1]) > 1
        }
        if _folded:
            out.append("\t\tJSONPerPartSQL: map[string]JSONPerPart{\n")
            for cls in sorted(_folded):
                chain = _folded[cls]["Chain"][0]
                call = _folded[cls].get("Call", ("", ""))[0]
                out.append(
                    f"\t\t\t{gostr(cls)}: {{{gostr(chain)}, {gostr(call)}}},\n"
                )
            out.append("\t\t},\n")
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
            # Only where a class writes the path DIFFERENTLY from the
            # standalone rendering. Databricks is the one: the same path is
            # `c1:item[1].price` under JSONExtract and `'$.item[1].price'`
            # under JSONExtractScalar.
            overrides = {}
            for cls_name in ("JSONExtract", "JSONExtractScalar"):
                got = json_path_pieces_in_class(name, exp, cls_name)
                if got and any(
                    got[k] != _jp.get(k, got["Key"])
                    for k in ("Key", "KeyAfter", "Subscript", "QuotedKey")
                ):
                    overrides[cls_name] = got
            if overrides:
                out.append("\t\tJSONPathByClass: map[string]JSONPathSQL{\n")
                for cls_name in sorted(overrides):
                    o = overrides[cls_name]
                    out.append(f"\t\t\t{gostr(cls_name)}: {{\n")
                    for k in ("Key", "KeyAfter", "Subscript", "QuotedKey", "Form", "PlainForm"):
                        out.append(f"\t\t\t\t{k}: {gostr(o[k])},\n")
                    out.append(
                        f"\t\t\t\tEscapesQuote: {str(o['EscapesQuote']).lower()},\n"
                    )
                    out.append("\t\t\t},\n")
                out.append("\t\t},\n")
        out.append(
            f"\t\tBracketIsRewritten: {str(bracket_is_rewritten(name)).lower()},\n"
        )
        out.append(f"\t\tIndexOffset: {d.INDEX_OFFSET},\n")
        out.append(
            f"\t\tSafeToEliminateDoubleNegation: "
            f"{str(bool(d.SAFE_TO_ELIMINATE_DOUBLE_NEGATION)).lower()},\n"
        )
        nested_kinds = {
            exp.DType[t.name].value
            for t in P.NESTED_TYPE_TOKENS
            if t.name in exp.DType.__members__
        }
        struct_kinds = {
            exp.DType[t.name].value
            for t in P.STRUCT_TYPE_TOKENS
            if t.name in exp.DType.__members__
        }
        for field, kinds in (
            ("NestedTypeKinds", nested_kinds),
            ("StructTypeKinds", struct_kinds),
        ):
            body = "".join(f"\t\t\t{gostr(k)}: true,\n" for k in sorted(kinds))
            out.append(f"\t\t{field}: map[string]bool{{\n{body}\t\t}},\n")
        out.append(
            f"\t\tSupportsFixedSizeArrays: {str(bool(d.SUPPORTS_FIXED_SIZE_ARRAYS)).lower()},\n"
        )
        _ct = composite_type_sql(name)
        out.append("\t\tCompositeType: CompositeTypeSQL{\n")
        for k in ("ArrayTemplate", "ArraySizedTemplate", "StructOpen", "StructClose", "StructFieldSep"):
            out.append(f"\t\t\t{k}: {gostr(_ct[k])},\n")
        out.append("\t\t},\n")
        _qw = quantifier_wraps_subquery(name)
        out.append("\t\tQuantifierWrapsSubquery: map[string]bool{\n")
        for k in sorted(_qw):
            out.append(f"\t\t\t{gostr(k)}: {str(_qw[k]).lower()},\n")
        out.append("\t\t},\n")
        _ph = placeholder_sql(name)
        out.append("\t\tPlaceholder: PlaceholderSQL{\n")
        for k in ("Named", "Anonymous", "Parameter", "DollarName", "DollarNumber",
                  "AtName", "PercentNamed", "PercentAnonymous", "AnonymousJDBCSQL"):
            out.append(f"\t\t\t{k}: {gostr(_ph[k])},\n")
        out.append(f"\t\t\tAnonymousJDBC: {str(_ph['AnonymousJDBC']).lower()},\n")
        out.append("\t\t},\n")
        _qq = quantifier_query_sql(name)
        out.append("\t\tQuantifierQuerySQL: map[string]string{\n")
        for k in sorted(_qq):
            out.append(f"\t\t\t{gostr(k)}: {gostr(_qq[k])},\n")
        out.append("\t\t},\n")
        _q = quantifier_sql(name, exp)
        if _q:
            out.append("\t\tQuantifierSQL: map[string]string{\n")
            for k in sorted(_q):
                out.append(f"\t\t\t{gostr(k)}: {gostr(_q[k])},\n")
            out.append("\t\t},\n")
        _wg = within_group_absorbed_by(name)
        if _wg:
            out.append("\t\tWithinGroupAbsorbedBy: map[string]bool{\n")
            for cls in _wg:
                out.append(f"\t\t\t{gostr(cls)}: true,\n")
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
        units = "".join(
            f"\t\t\t{gostr(u)}: {{}},\n" for u in sorted(d.VALID_INTERVAL_UNITS)
        )
        out.append(
            f"\t\tValidIntervalUnits: map[string]struct{{}}{{\n{units}\t\t}},\n"
        )
        texts = {}
        for text, token in Dialect.get_or_raise(name or None).tokenizer_class.KEYWORDS.items():
            texts.setdefault(token, []).append(text)
        _br = binary_range_ops(name, P, texts)
        body = "".join(
            f"\t\t\tTok{k}: {gostr(v['class'])},\n" for k, v in sorted(_br.items())
        )
        out.append(f"\t\tBinaryRangeOps: map[TokenType]string{{\n{body}\t\t}},\n")
        spellings = {v["class"]: v["op"] for v in _br.values() if v["op"]}
        body = "".join(f"\t\t\t{gostr(k)}: {gostr(v)},\n" for k, v in sorted(spellings.items()))
        out.append(f"\t\tBinaryRangeSQL: map[string]string{{\n{body}\t\t}},\n")
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
