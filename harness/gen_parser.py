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
from enum import Enum
import pathlib
import re
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


def isStringProbe(exp, node) -> bool:
    """Whether this probe argument is a STRING literal."""
    return isinstance(node, exp.Literal) and bool(node.args.get("is_string"))


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


def builder_words(builder):
    """The string constants a builder COMPARES its arguments against.

    T-SQL's HASHBYTES reads its first argument as the name of a digest -- MD5,
    SHA, SHA1, SHA2_256, SHA2_512 -- and builds a different class for each.
    The vocabulary is not something a generic probe can guess, and hand-typing
    it is hand-transcribing the builder. So it is read off the builder's own
    code, tuples included: `kind in ("SHA", "SHA1")` keeps its two words in a
    single constant, and a first attempt that read only the plain strings
    found MD5 and missed SHA entirely.
    """
    code = getattr(builder, "__code__", None)
    if code is None:
        return set()
    out: set = set()
    stack = [code]
    while stack:
        for c in stack.pop().co_consts:
            if isinstance(c, str):
                out.add(c)
            elif isinstance(c, tuple):
                out |= {v for v in c if isinstance(v, str)}
            elif hasattr(c, "co_consts"):
                stack.append(c)
    return {w for w in out if w and w.replace("_", "").isalnum()}


def _predicts(exp, builder, dialect, spec, subst, index, kind, same, rebuild, call_builder):
    """Whether a spec explains a SECOND argument of the same kind as the first.

    A builder that COMPUTES from what it is handed describes perfectly against
    one probe and wrongly against every other value. Nothing but a second
    probe tells that apart from a rule.
    """
    other_value = _another(exp, kind)
    if other_value is None:
        return True
    tried = list(subst)
    tried[index] = other_value
    try:
        built = call_builder(builder, list(tried), dialect)
    except Exception:  # noqa: BLE001
        return False
    return isinstance(built, exp.Expr) and same(rebuild(spec, tried), built, tried)


def _another(exp, kind):
    """A second value of the same kind as this probe, or None where there is
    no second one worth trying."""
    if isinstance(kind, exp.Literal):
        text = kind.args.get("this")
        if not kind.args.get("is_string"):
            return exp.Literal.number(7 if str(text) != "7" else 3)
        if isinstance(text, str) and text.isdigit():
            return exp.Literal.string("7")
        return exp.Literal.string("zzother")
    return None


def _kind_specs(
    exp, builder, width, dialect, describe, same, rebuild, call_builder, probe_kind
):
    """One arity's shape, per the KIND of one argument.

    PostgreSQL's REGEXP_REPLACE takes `(source, pattern, replacement [, start
    [, N ]] [, flags ])`, and the only thing telling a `start` from a `flags`
    is whether the last argument is a string that is not a number. So four
    arguments are two different shapes, and probed with columns alone the
    fourth always looked like a `start`.

    Recorded only where every kind either matches the base shape or yields a
    describable one of its own, and where the alternates for a kind agree with
    themselves. A builder that varies for some other reason yields nothing and
    the arity stays unrecorded, as it was.
    """
    args = [exp.column(f"__probe_{i}") for i in range(width)]
    try:
        base = call_builder(builder, list(args), dialect)
    except Exception:  # noqa: BLE001 -- not an arity this builder takes
        return None
    if not isinstance(base, exp.Expr):
        return None
    base_spec = describe(base, args)
    if base_spec is None:
        return None
    alternates: dict = {}
    # A string that spells a number goes with the rest: it is the one shape
    # the strict pass never offers, and the one this rule turns on.
    for kind in probe_substitutions(exp) + [exp.Literal.string("1")]:
        label = probe_kind(exp, kind)
        for i in range(width):
            subst = list(args)
            subst[i] = kind.copy()
            try:
                other = call_builder(builder, list(subst), dialect)
            except Exception:  # noqa: BLE001
                continue
            if not isinstance(other, exp.Expr):
                return None
            if other.__class__ is base.__class__ and same(
                rebuild(base_spec, subst), other, subst
            ):
                continue
            spec = describe(other, subst)
            if spec is None:
                return None
            found = (other.__class__.__name__, spec)
            # The alternate has to be a RULE, not a reading of this one probe.
            # T-SQL's DATEDIFF turns a number into a date -- 1 becomes
            # '1900-01-02' -- and described from a single probe that date came
            # out as a CONSTANT the port would then write for every number it
            # was given. So each alternate is put to a second value of the
            # same kind, and kept only if it predicts that one too.
            if not _predicts(
                exp, builder, dialect, spec, subst, i, kind, same, rebuild, call_builder
            ):
                return None
            if alternates.setdefault((i, label), found) != found:
                return None
    if not alternates:
        return None
    return {
        "base": (base.__class__.__name__, base_spec),
        "alternates": alternates,
    }


def _value_dispatch(exp, builder, args, index, dialect, describe, call_builder):
    """A builder that picks its CLASS from one argument's VALUE.

    The sibling of _type_dispatch, and the same shape: a spec per value rather
    than per type. What differs is where the candidates come from -- a type
    can be enumerated, a word cannot, so the words are read out of the
    builder's own constants.

    Recorded only where the words actually disagree with the default, so a
    builder that merely happens to hold strings yields nothing.
    """
    try:
        plain = call_builder(builder, list(args), dialect)
    except Exception:  # noqa: BLE001
        return None
    default = (type(plain).__name__, describe(plain, args))
    if default[1] is None:
        return None
    by_value: dict[str, tuple] = {}
    for word in sorted(builder_words(builder)):
        subst = list(args)
        subst[index] = exp.Literal.string(word)
        try:
            # The builder may CONSUME the word -- HASHBYTES pops it off -- so
            # it is handed a list of its own and described against this one.
            node = call_builder(builder, list(subst), dialect)
        except Exception:  # noqa: BLE001 -- not a word this builder takes
            continue
        if not isinstance(node, exp.Expr):
            continue
        if type(node).__name__ == default[0]:
            # Same class, so whatever changed is not a dispatch. Half the
            # constants in a builder are not vocabulary at all, and a first
            # attempt that kept every word whose RESULT differed recorded
            # T-SQL's DATENAME dispatching on "TSQL" -- the module name, read
            # as a date part and translated like one.
            continue
        spec = describe(node, subst)
        if spec is None:
            continue
        by_value[word.upper()] = (type(node).__name__, spec)
    if not by_value:
        return None
    # A VOCABULARY, or a rule that merely happens to hold strings? T-SQL's
    # FORMAT picks TimeToStr or NumberToStr by running a REGEX over the format
    # string, and its constants -- "TSQL", "THIS", "FORMAT" -- are module
    # names that happen to look like date formats to that regex. Recorded as a
    # word list it would have read FORMAT(x, 'yyyy') as a number.
    #
    # So the list is only a list if words OUTSIDE it take the default. The
    # controls are shapes a caller actually writes, not nonsense alone.
    for control in ("ZZQQ", "yyyy", "hh", "d", "0", "#,##0.00"):
        if control.upper() in by_value:
            continue
        subst = list(args)
        subst[index] = exp.Literal.string(control)
        try:
            node = call_builder(builder, list(subst), dialect)
        except Exception:  # noqa: BLE001
            continue
        if isinstance(node, exp.Expr) and type(node).__name__ != default[0]:
            return None
    return {"index": index, "default": default, "by_value": by_value}


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


def string_wrap(exp, spec, subst, index, literal, other, same, rebuild, call_builder):
    """How a builder REWRITES a string argument, if that is all it did.

    Two builders in the catalogue take a string and put something else in the
    slot: T-SQL's DATETRUNC casts `'foo'` to DATETIME2, and PostgreSQL's
    GENERATE_SERIES turns a step of `'1 day'` into `INTERVAL '1' DAY`. Probed
    with a placeholder column neither shows, and probed with a string both look
    like builders nobody can describe -- so both names were refused at every
    arity.

    The candidates are not guessed at: the CAST target is read out of the node
    the builder actually produced, and the interval is the reference's own
    `to_interval`. A rewrite that neither explains is still a builder this
    cannot describe, and the name stays refused.
    """
    candidates = []
    for node in other.walk():
        if isinstance(node, exp.Cast) and node.this == literal:
            # The TYPE, not a dialect's way of writing it: T-SQL's DATETIME2
            # renders as TIMESTAMP in the neutral dialect, and recording the
            # render would have the port build a cast to the wrong type.
            kind = getattr(node.to.this, "value", None)
            if isinstance(kind, str):
                candidates.append(("cast:" + kind, exp.cast(literal.copy(), node.to.copy())))
    if any(isinstance(node, exp.Interval) for node in other.walk()):
        try:
            candidates.append(("interval", exp.to_interval(literal.this)))
        except Exception:  # noqa: BLE001 -- not a string an interval can be made of
            pass
    for label, replacement in candidates:
        tried = list(subst)
        tried[index] = replacement
        if same(rebuild(spec, tried), other, tried):
            return label
    return None


def self_cast_exceptions(exp, spec, args, builder, dialect, same, rebuild, call_builder):
    """Cast targets an argument may ALREADY carry, which skip the cast.

    `exp.cast` does not cast twice: hand Databricks' FROM_UTC_TIMESTAMP an
    `x::TIMESTAMP` and it leaves it alone rather than wrapping it again. None
    of the generic probes can show that, because the type that matters is the
    wrapper's OWN -- and the dialect's reading of it, which need not be the
    same word: Databricks reads TIMESTAMP as TIMESTAMPTZ.

    Recorded as `cast:TYPE`, so the port makes the same test on its own tree.
    """
    import sqlglot

    for pos, (key, how) in enumerate(spec):
        if how.get("nested") != "Cast":
            continue
        target = None
        for _, inner in how["spec"]:
            if inner.get("node") == "DataType":
                for k, v in inner.get("extras") or ():
                    if k == "this" and isinstance(v, Enum):
                        target = v.value
        held = nested_index(how)
        if target is None or held is None or held >= len(args):
            continue
        # The name as written, and whatever this dialect turns it into.
        candidates = {target}
        try:
            read = sqlglot.parse_one(f"CAST(zz AS {target})", dialect=dialect or None)
            kind = getattr(read.to.this, "value", None)
            if isinstance(kind, str):
                candidates.add(kind)
        except Exception:  # noqa: BLE001 -- not a type this dialect writes
            pass
        stripped = list(spec)
        stripped[pos] = (key, {"index": held})
        for kind in sorted(candidates):
            tried = list(args)
            tried[held] = exp.cast(exp.column("__already"), kind)
            try:
                other = call_builder(builder, tried, dialect)
            except Exception:  # noqa: BLE001
                continue
            if not isinstance(other, exp.Expr):
                continue
            if same(rebuild(stripped, tried), other, tried):
                how.setdefault("except", set()).add("cast:" + kind)


def annot_of(node):
    """The TYPE a builder annotated the node it made with, if it did.

    `exp.cast` records its target on the node as well as inside the cast, and
    the reference dumps that annotation. It shows in no SQL, so nothing but
    the differential catches a port that leaves it off.
    """
    annot = getattr(node, "type", None)
    if annot is None or not all(is_scalar(v) for v in annot.args.values()):
        return None
    return {"node": type(annot).__name__, "extras": list(annot.args.items())}


def goannot(annot) -> str:
    """An annotation as the FuncArg the port reads it from, or nil."""
    if not annot:
        return "nil"
    extras = "".join(
        "{%s, %s}, " % (gostr(k), goconst(v)) for k, v in (annot.get("extras") or [])
    )
    return '&FuncArg{"this", -1, false, nil, %s, []FuncConst{%s}, "", nil, nil, nil}' % (
        gostr(annot["node"]),
        extras,
    )


def is_scalar(v) -> bool:
    """A value a spec can carry verbatim.

    An ENUM counts. A DataType holds its kind as `DType.TEXT`, which is
    neither a string nor a node, so every builder that CASTS an argument --
    T-SQL's LEN, LEFT and RIGHT, Databricks' FROM_UTC_TIMESTAMP -- described
    down to the cast's target and then stopped, and the whole name was
    refused as undescribable.
    """
    return v is None or isinstance(v, (bool, str, int)) or isinstance(v, Enum)


def probe_kind(exp, node):
    """The argument KIND a builder may branch on, named as the port names it.

    Only distinctions the port can make on its own tree, because it has to
    make the same one at parse time with no reference to hand.
    """
    if isinstance(node, exp.Literal):
        if not node.args.get("is_string"):
            return "number"
        # A string that spells a NUMBER is its own kind. PostgreSQL's
        # REGEXP_REPLACE reads a trailing string as flags -- unless it spells
        # an integer, when it is a position -- so the two cannot share a name
        # or the port would route `'1'` where `'g'` goes.
        text = node.args.get("this")
        return "digits" if isinstance(text, str) and text.isdigit() else "string"
    if isinstance(node, exp.Cast):
        return "cast"
    if isinstance(node, exp.Subquery):
        return "subquery"
    return "call"


def nested_index(how):
    """The argument position a nested wrapper holds, if it holds just one."""
    held = [inner["index"] for _, inner in how["spec"] if "index" in inner]
    return held[0] if len(held) == 1 else None


def nested_exception(spec, subst, index, other, same, rebuild, kind):
    """A nested wrapper the builder puts on SOME arguments and not others.

    T-SQL's LEN casts its argument to TEXT -- unless the argument is already
    a string, when it leaves it alone. Probed with a column the cast is part
    of the spec; probed with a string it is gone, and until now that counted
    as a builder nobody could describe, so LEN was refused at every arity.

    It is not undescribable, it is a RULE: the wrapper has an exception. What
    this returns is the position in the spec whose wrapper the substituted
    argument escaped, so the caller can record which kinds escape it. A
    difference the stripped spec does not explain is still undescribable, and
    the name stays refused.
    """
    for pos, (key, how) in enumerate(spec):
        if "nested" not in how:
            continue
        if not any(inner.get("index") == index for _, inner in how["spec"]):
            continue
        stripped = list(spec)
        stripped[pos] = (key, {"index": index})
        if same(rebuild(stripped, subst), other, subst):
            how.setdefault("except", set()).add(kind)
            return pos
    return None


def probe_functions(P, exp, dialect="", branch_classes=None, format_args=None):
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
    by_value: dict = {}
    kind_specs: dict = {}

    def placeholders(n):
        return [exp.column(f"__probe_{i}") for i in range(n)]

    def describe(node, args):
        """Map each of the node's args to a placeholder position or a constant."""
        by_id = {id(a): i for i, a in enumerate(args)}

        def position(value):
            """Which argument this is, if it is one.

            Identity is not enough. `exp.cast` COPIES what it casts, so the
            column inside T-SQL's `LEN(x)` -> `Length(Cast(x, TEXT))` is not
            the object handed to the builder -- and describing it structurally
            instead wrote the placeholder's own name into the spec, which is
            right for the probe and nonsense for real SQL. Every builder that
            copies an argument into a wrapper was refused over this.
            """
            if id(value) in by_id:
                return by_id[id(value)]
            for i, a in enumerate(args):
                if type(a) is type(value) and a == value:
                    return i
            return None

        out = []
        for key, value in node.args.items():
            at = position(value)
            if at is not None:
                how = {"index": at}
                # The builder may ANNOTATE the argument it was handed rather
                # than wrap it: PostgreSQL marks REGEXP_REPLACE's flags as a
                # VARCHAR. It shows in no SQL and in no argument, only in the
                # dump -- so only the differential ever saw it missing.
                # A CAST annotates itself -- its target IS its type -- so an
                # annotation there is the caller's, not the builder's, and
                # recording it made one spec per cast target and no agreement
                # between them. Every other class carries none of its own, so
                # an annotation on one is the builder's doing.
                annot = None if isinstance(value, exp.Cast) else annot_of(value)
                if annot is not None:
                    how["annot"] = annot
                out.append((key, how))
            elif isinstance(value, list):
                indexes = [position(v) for v in value]
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
            elif is_scalar(value):
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
                    (k, v) for k, v in value.args.items() if k != "this" and is_scalar(v)
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
                elif all(is_scalar(v) for v in value.args.values()):
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
                    # A node built AROUND the arguments rather than from one
                    # of them: DuckDB's ANY_VALUE returns
                    # IgnoreNulls(AnyValue(args[0])), and Databricks' MONTH
                    # returns Month(TsOrDsToDate(args[0])). The wrapper is
                    # fixed and the inside is describable, so describe it and
                    # keep both -- rejecting the name outright turned away a
                    # whole family of date parts.
                    inner_spec = describe(value, args)
                    if inner_spec is None:
                        return None
                    how = {"nested": type(value).__name__, "spec": inner_spec}
                    # A builder that CASTS also ANNOTATES what it built:
                    # `exp.cast` records the target on the node as well as in
                    # the cast. The reference dumps that annotation, so a port
                    # that only built the cast produced a tree the differential
                    # called different -- over a field nothing in the SQL shows.
                    annot = annot_of(value)
                    if annot is not None:
                        how["annot"] = annot
                    out.append((key, how))
            else:
                return None
        return out

    def rebuild(spec, args):
        out = {}
        for key, how in spec:
            if "nested" in how:
                # A wrapper with an exception is skipped for the argument
                # kinds that escape it, exactly as the port will skip it.
                held = nested_index(how)
                if (
                    how.get("except")
                    and held is not None
                    and held < len(args)
                    and probe_kind(exp, args[held]) in how["except"]
                ):
                    out[key] = args[held]
                    continue
                made = getattr(exp, how["nested"])(**rebuild(how["spec"], args))
                if "annot" in how:
                    made.type = getattr(exp, how["annot"]["node"])(
                        **dict(how["annot"]["extras"])
                    )
                out[key] = made
            elif "node" in how:
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
                held = args[how["index"]] if how["index"] < len(args) else None
                if held is not None and "annot" in how:
                    held = held.copy()
                    held.type = getattr(exp, how["annot"]["node"])(
                        **dict(how["annot"]["extras"])
                    )
                out[key] = held
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
            elif is_scalar(value):
                if actual is not value and actual != value:
                    return False
            elif isinstance(value, exp.Expr) and isinstance(actual, exp.Expr):
                # A wrapped node is rebuilt, not carried, so identity is the
                # wrong test -- compare what it is instead.
                if type(value) is not type(actual) or value.args != actual.args:
                    return False
                if getattr(value, "type", None) != getattr(actual, "type", None):
                    return False
            elif isinstance(value, exp.Expr) and isinstance(actual, exp.Expr):
                if type(value) is not type(actual) or value.args != actual.args:
                    return False
                if getattr(value, "type", None) != getattr(actual, "type", None):
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

    # The classes a nested one-argument call can have. A substitution whose
    # class is one of these and that moves the builder's own class is not an
    # undescribable builder: it is a position that BRANCHES on a class, and
    # ClassSensitiveArgs records it separately so the parser can refuse just
    # those calls. Rejecting the whole name over it left UPPER and LOWER --
    # two of the commonest functions there are -- with no signature at any
    # arity, and every plain UPPER(x) refused.
    if branch_classes is None:
        branch_classes = class_candidates(P, exp, dialect)
    # Positions that hold a TIME FORMAT. A string there is REWRITTEN by the
    # builder -- `'YYYY-MM-DD'` becomes `'%Y-%m-%d'` -- so the spec probed with
    # a column does not explain it, and the name was refused at every arity.
    # The port rewrites the literal itself before building, from the table
    # time_format_args produces, so a string that only moves in that way is
    # not evidence of a builder nobody can describe.
    format_args = format_args or {}

    out = {}
    by_arity: dict[str, dict[int, tuple]] = {}
    unit_maps: dict[str, dict] = {}
    # The annotation on the spec's OWN class, where the builder's outermost
    # node is the cast: PostgreSQL's DIV is a Cast over an IntDiv.
    root_annots: dict = {}
    wraps: dict[str, dict[int, str]] = {}
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
                    if isStringProbe(exp, kind) and i in format_args.get(name, ()):
                        continue
                    if isStringProbe(exp, kind):
                        label = string_wrap(
                            exp, spec, subst, i, subst[i], other, same, rebuild, call_builder
                        )
                        if label is not None and wraps.setdefault(name, {}).get(i, label) == label:
                            wraps[name][i] = label
                            continue
                    if nested_exception(
                        spec, subst, i, other, same, rebuild, probe_kind(exp, kind)
                    ) is not None:
                        continue
                    ok = False
                    break
            if not ok:
                break
        # And once with EVERY argument a string at the same time. PostgreSQL's
        # JSON_EXTRACT_PATH only builds a JSON path when all of its arguments
        # are strings; with placeholder columns, or with one string among
        # columns, it hands them back unchanged and looks perfectly ordinary.
        # A name with a FORMAT position is not put to it: the format is
        # rewritten there by design, so every all-strings call differs from
        # the spec and would reject the name for the one thing the port
        # already knows how to do.
        if ok and args and not any(i < len(args) for i in format_args.get(name, ())) \
                and not wraps.get(name):
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
        if not ok and name not in dispatch:
            # A class chosen by a WORD rather than by a type. Every position
            # is offered, because which one holds the word is the builder's
            # business, not the probe's.
            for i in range(len(args)):
                entry = _value_dispatch(
                    exp, builder, args, i, dialect, describe, call_builder
                )
                if entry is not None:
                    by_value[name] = entry
                    break
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
            self_cast_exceptions(
                exp, spec, args, builder, dialect, same, rebuild, call_builder
            )
            out[name] = (node.__class__.__name__, spec)
            root_annots[name] = annot_of(node)
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
                    if not isinstance(other, exp.Expr):
                        fine = False
                        break
                    if other.__class__ is not one.__class__:
                        if type(kind).__name__ in branch_classes:
                            # A branch ClassSensitiveArgs already records.
                            continue
                        fine = False
                        break
                    if not same(rebuild(narrow_spec, subst), other, subst):
                        if isStringProbe(exp, kind) and i in format_args.get(name, ()):
                            continue
                        if isStringProbe(exp, kind):
                            label = string_wrap(
                                exp, narrow_spec, subst, i, subst[i], other,
                                same, rebuild, call_builder,
                            )
                            if label is not None and wraps.setdefault(name, {}).get(i, label) == label:
                                wraps[name][i] = label
                                continue
                        if nested_exception(
                            narrow_spec, subst, i, other, same, rebuild,
                            probe_kind(exp, kind),
                        ) is not None:
                            continue
                        fine = False
                        break
                if not fine:
                    break
            if fine and width and not any(i < width for i in format_args.get(name, ())) \
                    and not wraps.get(name):
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
                self_cast_exceptions(
                    exp, narrow_spec, narrow, builder, dialect, same, rebuild, call_builder
                )
                by_arity.setdefault(name, {})[width] = (one.__class__.__name__, narrow_spec)
                root_annots[(name, width)] = annot_of(one)
                aliases = unit_aliases(builder)
                if aliases:
                    unit_maps[name] = aliases
        # An arity the loop above left out because ONE argument's kind moves
        # the shape. Filled here rather than there so the strict pass stays
        # strict: what this records is an exception it names, not a relaxation.
        recorded = by_arity.get(name, {})
        if name not in out:
            for width in range(13):
                if width in recorded:
                    continue
                found = _kind_specs(
                    exp, builder, width, dialect, describe, same, rebuild,
                    call_builder, probe_kind,
                )
                if found is None:
                    continue
                by_arity.setdefault(name, {})[width] = found["base"]
                root_annots[(name, width)] = None
                for (i, label), variant in found["alternates"].items():
                    kind_specs.setdefault(name, {}).setdefault(width, {})[
                        (i, label)
                    ] = variant

    return out, by_arity, unit_maps, dispatch, wraps, root_annots, by_value, kind_specs


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


def hoists_insert_with(dialect: str) -> bool:
    """Whether a WITH inside an INSERT is written in FRONT of the statement.

    T-SQL writes `WITH cte AS (...) INSERT INTO t SELECT ...` for the tree
    that has the WITH on the inner query; the neutral dialect leaves it where
    it was written. Same tree, two spellings.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "INSERT INTO x WITH y AS (SELECT 1) SELECT * FROM y", read=dialect or None
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return False
    return text.upper().startswith("WITH")


def returning_conventions(dialect: str) -> tuple[str, bool]:
    """How a RETURNING clause is spelled, and whether it comes LAST.

    T-SQL calls it OUTPUT and writes it early -- straight after SET in an
    UPDATE, straight after the verb in a DELETE -- where everyone else writes
    it after the WHERE. One trait with two consequences, so both statements
    are asked and the answers have to agree.
    """
    import sqlglot

    try:
        update = sqlglot.parse_one(
            "UPDATE t SET a = 1 WHERE b = 2 RETURNING zzret", read="postgres"
        ).sql(dialect=dialect or None)
        delete = sqlglot.parse_one(
            "DELETE FROM t WHERE b = 2 RETURNING zzret", read="postgres"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return "RETURNING", True

    # The clause is not necessarily last, so the word is the one in FRONT of
    # the marker rather than the second from the end.
    head = update.split("zzret")[0].split()
    word = head[-1] if head else "RETURNING"
    update_last = update.rstrip().endswith("zzret")
    delete_last = delete.rstrip().endswith("zzret")
    assert update_last == delete_last, (
        "%s writes RETURNING last in one statement and not the other" % dialect
    )
    return word, update_last


def cast_coercions(exp, dialect, cls_name, keys):
    """What the dialect WRAPS an argument in, per the type it is cast to.

    DuckDB writes `BIT_OR(CAST(ROUND(CAST(v AS REAL)) AS INT))` for a float and
    `BIT_OR(CAST(CAST(v AS DECIMAL) AS INT))` for a decimal -- one rounds and
    the other does not -- and `BIT_OR(CAST(v AS INT))` for an integer, which it
    leaves alone. So the wrapper is not one template but one per type, and each
    is read off by rendering the call over an argument of that type and
    replacing the argument with a marker.
    """
    cls = getattr(exp, cls_name, None)
    if cls is None:
        return {}
    out = {}
    for key in keys:
        # The SIBLINGS matter. PostgreSQL's ROUND adds its cast only when a
        # number of decimals was given -- with one argument it returns early
        # -- so the call is probed both bare and with every other argument
        # filled, and the shape that shows a wrapper is the one kept.
        others = {}
        for k in cls.arg_types:
            if k == key:
                continue
            token = "ZZ" + k.upper() + "ZZ"
            probe = exp.column(token)
            try:
                # A key that does not APPEAR when it is filled is a flag
                # rather than an argument, and counting it would put the
                # wrapper under an arity no call ever has.
                if token not in cls(**{key: exp.column("ZZARGZZ"), k: probe}).sql(
                    dialect=dialect or None
                ):
                    continue
            except Exception:  # noqa: BLE001
                continue
            others[k] = probe
        for siblings in ({}, others) if others else ({},):
            found = _wrappers_for(exp, cls, dialect, key, siblings)
            if not found:
                continue
            out.setdefault(1 + len(siblings), {})[key] = found
    return out


def _wrappers_for(exp, cls, dialect, key, siblings):
    """The wrapper this call puts round one argument, per the type it is cast
    to, with the other arguments filled as given."""
    per_type = {}
    # A STRING LITERAL is coerced by what it IS rather than by a cast it
    # carries: DuckDB writes `DATE_DIFF('QUARTER', b, CAST('2009-02-13' AS
    # DATE))` for a date written as text. It is asked about under a name of
    # its own, beside the cast targets.
    for target in ("STRING_LITERAL",) + CAST_PROBE_TYPES:
        try:
            if target == "STRING_LITERAL":
                arg = exp.Literal.string("ZZARGZZ")
            else:
                arg = exp.cast(exp.column("ZZARGZZ"), target)
            rendered = arg.sql(dialect=dialect or None)
            text = cls(**siblings, **{key: arg}).sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001 -- not a type this call takes
            continue
        if text.count(rendered) != 1:
            continue
        # What is left once the call's own spelling is taken off both
        # sides is the wrapper, with the argument standing in it.
        plain = cls(**siblings, **{key: exp.column("ZZARGZZ")})
        try:
            bare = plain.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        head, _, tail = bare.partition("ZZARGZZ")
        if not text.startswith(head) or not text.endswith(tail):
            continue
        middle = text[len(head) : len(text) - len(tail)] if tail else text[len(head) :]
        wrapper = middle.replace(rendered, "{arg}")
        # The argument has to be FOUND in what is left, or the wrapper is
        # the probe's own text rather than a template. That happens where
        # the call's plain spelling already carries the cast -- the two
        # renderings cancel and nothing is left to mark -- and those slots
        # are the idempotent ones, recorded elsewhere.
        if "{arg}" not in wrapper:
            continue
        per_type[target] = wrapper
    return per_type


def _recast_probe(exp, cls, kwargs, expr_keys, scalars, dialect):
    """Re-probe a shape whose operands the reference CASTS, with them cast.

    DuckDB writes `BOOL_OR(CAST(x AS BOOLEAN))` for any operand, so the plain
    probe records a cast it would then apply a second time. An operand that
    already carries the cast is written as it stands, and that rendering is a
    template the port can use -- for those operands and no others.
    """
    import re as _re

    args = dict(scalars)
    types = {}
    for k in expr_keys:
        bare = k[:-2] if k.endswith("[]") else k
        token = f"ZZ{bare.upper()}ZZ"
        probe = exp.column(token)
        args[bare] = [probe] if k.endswith("[]") else probe
    try:
        plain = _render(exp, cls(**args), dialect)
    except Exception:  # noqa: BLE001
        return None
    for k in expr_keys:
        bare = k[:-2] if k.endswith("[]") else k
        token = f"ZZ{bare.upper()}ZZ"
        m = _re.search(r"CAST\(" + token + r" AS ([A-Z0-9_ ]+)\)", plain)
        if m:
            types[bare] = m.group(1)
    if not types:
        return None
    # The dialect absorbs a FAMILY of types, not one: Databricks leaves both
    # `CAST(x AS TIMESTAMP)` and `CAST(x AS TIMESTAMPTZ)` alone and wraps
    # everything else. Each candidate is tried and the ones that come back
    # with no cast added are the ones the slot accepts.
    absorbed = {}
    for bare, kind in types.items():
        keep = []
        for candidate in (kind,) + CAST_PROBE_TYPES:
            if candidate in keep:
                continue
            try:
                probe = exp.cast(exp.column(f"ZZ{bare.upper()}ZZ"), candidate)
                trial = dict(args)
                trial[bare] = probe
                rendered = _render(exp, cls(**trial), dialect)
            except Exception:  # noqa: BLE001 -- a type this dialect will not take here
                continue
            if rendered.count("CAST(") == 1 and probe.sql(dialect=dialect or None) in rendered:
                keep.append(candidate)
        if not keep:
            return None
        absorbed[bare] = keep
        args[bare] = exp.cast(exp.column(f"ZZ{bare.upper()}ZZ"), keep[0])
    types = absorbed
    try:
        text = _render(exp, cls(**args), dialect)
    except Exception:  # noqa: BLE001
        return None
    bracketed = []
    for k in expr_keys:
        bare = k[:-2] if k.endswith("[]") else k
        rendered = args[bare].sql(dialect=dialect or None) if bare in absorbed else None
        marker = rendered if rendered else f"ZZ{bare.upper()}ZZ"
        if text.count(marker) != 1:
            return None
        text = text.replace(marker, "{" + bare + "}")
    if "CAST(" in text:
        return None
    return text, bracketed, types


def _create_body(kind: str) -> str:
    """What follows the name in the canonical CREATE used by the probes below."""
    if kind in ("SCHEMA", "DATABASE", "NAMESPACE"):
        return ""
    if kind == "TABLE":
        return " (a INT)"
    if kind in ("FUNCTION", "PROCEDURE"):
        return "() AS \'b\'"
    if kind == "INDEX":
        return " ON zztbl(zzc)"
    return " AS SELECT 1"


def create_exists_written(dialect: str) -> dict:
    """Whether `IF NOT EXISTS` survives, per kind.

    Asked by rendering the same statement twice, with the guard and without,
    and checking the only difference is the words themselves. T-SQL drops them
    from a VIEW -- which turns "make this if it is missing" into "make this" --
    and rewrites a TABLE into a conditional EXEC.
    """
    import sqlglot

    out = {}
    # Every kind the port reads, not only the four a table takes: a PROCEDURE
    # carries the guard as readily as a TABLE does, and a kind left out of the
    # probe was refused for a fact nobody had asked about.
    # Every kind this dialect creates, taken from the same probe the parser
    # reads its kinds from, so the two cannot drift: a kind the parser learns
    # to read and this does not ask about is refused on the way out.
    kinds = set(create_words(dialect)[1]) | {
        "TABLE", "VIEW", "INDEX", "SCHEMA", "FUNCTION", "PROCEDURE",
    }
    for kind in sorted(kinds):
        body = _create_body(kind)
        try:
            guarded = sqlglot.parse_one(
                f"CREATE {kind} IF NOT EXISTS zzname{body}"
            ).sql(dialect=dialect or None)
            plain = sqlglot.parse_one(f"CREATE {kind} zzname{body}").sql(
                dialect=dialect or None
            )
        except Exception:  # noqa: BLE001
            out[kind] = False
            continue
        want = plain.replace(f"CREATE {kind} ", f"CREATE {kind} IF NOT EXISTS ", 1)
        out[kind] = guarded == want
    return out


def temporary_written(dialect: str) -> dict:
    """Whether TEMPORARY survives unchanged, per kind.

    The same two-renderings question. T-SQL has no such modifier and renames
    the object to `#zzname` instead; Databricks writes a temporary TABLE with
    a storage format it was never given. Both say something the statement did
    not.
    """
    import sqlglot

    out = {}
    for kind in ("TABLE", "VIEW", "FUNCTION"):
        body = _create_body(kind)
        try:
            temp = sqlglot.parse_one(f"CREATE TEMPORARY {kind} zzname{body}").sql(
                dialect=dialect or None
            )
            plain = sqlglot.parse_one(f"CREATE {kind} zzname{body}").sql(
                dialect=dialect or None
            )
        except Exception:  # noqa: BLE001
            out[kind] = False
            continue
        out[kind] = temp == plain.replace("CREATE ", "CREATE TEMPORARY ", 1)
    return out


def temporary_suffix(dialect: str) -> dict:
    """What a dialect APPENDS to a temporary object of each kind.

    Databricks writes a temporary TABLE with a storage format it was never
    given -- `CREATE TEMPORARY TABLE t (a INT) USING PARQUET` -- and writes it
    a second time where the statement supplied one of its own. That is the
    whole of the difference, so it is measured as a suffix rather than left as
    a refusal.
    """
    import sqlglot

    out = {}
    for kind in ("TABLE", "VIEW", "FUNCTION"):
        body = _create_body(kind)
        try:
            temp = sqlglot.parse_one(f"CREATE TEMPORARY {kind} zzname{body}").sql(
                dialect=dialect or None
            )
            plain = sqlglot.parse_one(f"CREATE {kind} zzname{body}").sql(
                dialect=dialect or None
            )
        except Exception:  # noqa: BLE001
            continue
        head = plain.replace("CREATE ", "CREATE TEMPORARY ", 1)
        if temp.startswith(head) and temp != head:
            out[kind] = temp[len(head) :]
    return out


def view_column_comment_written(dialect: str) -> bool:
    """Whether a view column's COMMENT survives being written.

    PostgreSQL and DuckDB drop it, which loses what the column is FOR.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE VIEW z (a, b COMMENT 'zzdesc')").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return "zzdesc" in text


def alter_add_conventions(dialect: str) -> tuple[bool, bool]:
    """How an ALTER writes the columns it ADDS.

    Two questions, asked of one rendering of two added columns. T-SQL says
    neither the word COLUMN nor a second ADD -- `ADD a INT, b INT` -- where
    everyone else says both.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "ALTER TABLE t ADD COLUMN zza INT, ADD COLUMN zzb INT"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return True, True
    return "COLUMN zza" in text, text.count("ADD ") == 2


def alter_column_type_word(dialect: str) -> str:
    """What comes between an altered column and its NEW type.

    `SET DATA TYPE` almost everywhere, `TYPE` in Databricks, and nothing at
    all in T-SQL. Read off a rendering, because the phrase lives in the
    dialect's writer rather than in any table.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "ALTER TABLE t ALTER COLUMN zzcol SET DATA TYPE BIGINT"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return "SET DATA TYPE"
    head = text.find("zzcol ")
    tail = text.find("BIGINT")
    if head < 0 or tail < 0 or tail < head:
        return "SET DATA TYPE"
    return text[head + len("zzcol ") : tail].strip()


def primary_key_members_ordered(dialect: str) -> bool:
    """Whether a table-level PRIMARY KEY's members are ORDERED rather than bare.

    T-SQL reads them the way it reads an index -- each column may carry a
    direction -- so a member is an Ordered over a Column there and a plain
    Identifier everywhere else. Same statement, two shapes.
    """
    import sqlglot
    from sqlglot import exp

    try:
        tree = sqlglot.parse_one(
            "CREATE TABLE t (a INT, PRIMARY KEY (a, b))", read=dialect or None
        )
        key = next(tree.find_all(exp.PrimaryKey))
    except Exception:  # noqa: BLE001
        return False
    return any(isinstance(member, exp.Ordered) for member in key.expressions)


def unique_constraint_written(dialect: str) -> bool:
    """Whether a UNIQUE constraint survives being written.

    Databricks has no such constraint and the reference DROPS it, which loses
    the guarantee the statement was making.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE TABLE z (a INT UNIQUE)").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return "UNIQUE" in text.upper()


def _returns_shapes(exp):
    """The three shapes a RETURNS property's operand takes."""
    return {
        "type": exp.ReturnsProperty(this=exp.DataType.build("INT"), is_table=False),
        "table": exp.ReturnsProperty(this=exp.Var(this="TABLE"), is_table=True),
        "schema": exp.ReturnsProperty(
            this=exp.Schema(this=exp.Var(this="TABLE")), is_table=True
        ),
    }


def function_returns_place(dialect: str) -> dict:
    """WHERE each shape of a RETURNS property is written, per shape.

    Three answers. Most dialects write it after the parameter list, as
    `RETURNS INT`. DuckDB writes no return type at all -- except the one shape
    it spells in the body instead, `AS TABLE SELECT ...` -- so the same
    property is written in three different places depending on the dialect and
    on what it holds.
    """
    from sqlglot import exp

    out = {}
    for name, prop in _returns_shapes(exp).items():
        node = exp.Create(
            this=exp.UserDefinedFunction(this=exp.to_table("zzf"), wrapped=True),
            kind="FUNCTION",
            properties=exp.Properties(expressions=[prop]),
            expression=exp.Literal.string("zzbody"),
        )
        try:
            text = node.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            out[name] = ""
            continue
        head, _, tail = text.partition("zzf()")
        if "RETURNS" in tail.split("zzbody")[0].split(" AS ")[0]:
            out[name] = "schema"
        elif "TABLE" in tail.split("zzbody")[0]:
            out[name] = "alias"
        else:
            out[name] = ""
        del head
    return out


def function_properties_written(dialect: str) -> bool:
    """Whether a function's properties other than its return type survive.

    DuckDB writes none of them -- LANGUAGE, IMMUTABLE, STRICT and the rest all
    vanish -- so a function carrying one cannot be written there without
    saying less than the statement did.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE FUNCTION zzf() LANGUAGE zzlang AS \'b\'").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return "zzlang" in text


def function_return_as(dialect: str) -> bool:
    """Whether AS is written in front of a RETURN body.

    Databricks writes `RETURNS INT RETURN x`; everyone else writes
    `RETURNS INT AS RETURN x`.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE FUNCTION zzf() RETURNS INT AS RETURN zzbody").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return True
    return " AS " in text


def function_wraps_table_body(dialect: str) -> bool:
    """Whether a table-valued function's body becomes a RETURN even when it
    was not written as one.

    Databricks does that before writing, so a literal body comes out as
    `RETURN 'b'` rather than as `AS 'b'`. It is a change to the tree rather
    than a spelling, so a body written any other way is refused here.
    """
    from sqlglot import exp

    node = exp.Create(
        this=exp.UserDefinedFunction(this=exp.to_table("zzf"), wrapped=True),
        kind="FUNCTION",
        properties=exp.Properties(
            expressions=[exp.ReturnsProperty(this=exp.Var(this="TABLE"), is_table=True)]
        ),
        expression=exp.Literal.string("zzbody"),
    )
    try:
        text = node.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return False
    return "RETURN \'zzbody\'" in text


def return_word(dialect: str) -> str:
    """The word a RETURN writes. DuckDB writes none, leaving the body bare."""
    from sqlglot import exp

    try:
        text = exp.Return(this=exp.column("zzbody")).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return "RETURN"
    return text.replace("zzbody", "").strip()


def function_as_table_read(dialect: str) -> bool:
    """Whether `AS TABLE <query>` is read as a function's return type.

    DuckDB's spelling. Elsewhere the words are not a return type at all, so
    reading them as one would build a property the reference never made.
    """
    import sqlglot
    from sqlglot import exp

    try:
        tree = sqlglot.parse_one(
            "CREATE FUNCTION zzf() AS TABLE SELECT 1", read=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return any(True for _ in tree.find_all(exp.ReturnsProperty))


def parameter_mode(dialect: str) -> tuple[bool, dict]:
    """Where a parameter's MODE is written, and how it is spelled.

    PostgreSQL writes it in front of the parameter and spells both directions
    as one word, `INOUT`; everywhere else it follows the type and is two,
    `IN OUT`. Same node, two places and two spellings.
    """
    import sqlglot

    prefix = True
    words = {}
    for key, src in (
        ("in", "IN"),
        ("out", "OUT"),
        ("inout", "INOUT"),
        ("variadic", "VARIADIC"),
    ):
        try:
            text = sqlglot.parse_one(
                f"CREATE FUNCTION zzf({src} zzp INT)", read="postgres"
            ).sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            words[key] = src
            continue
        inside = text[text.index("(") + 1 : text.rindex(")")]
        head, _, tail = inside.partition("zzp")
        prefix = bool(head.strip())
        words[key] = (head if prefix else tail.split(" ", 2)[-1]).strip()
    return prefix, words


def set_item_separator(dialect: str) -> str:
    """What sits between a configuration name and its value.

    `SET search_path = 'public'` almost everywhere; T-SQL writes the two side
    by side with nothing between them.
    """
    import sqlglot

    # Asked of a SET STATEMENT rather than of one inside a function: DuckDB
    # writes a function's properties nowhere, so the function form measured
    # the absence of the whole clause instead of the separator in it.
    try:
        text = sqlglot.parse_one("SET zzname TO 'zzvalue'").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return " = "
    head, _, tail = text.partition("zzname")
    del head
    return tail.split("'zzvalue'")[0]


def computed_column_spelling(dialect: str) -> tuple[str, bool]:
    """How a COMPUTED column is written, and whether its type survives.

    Four spellings for one node. PostgreSQL keeps the whole
    `GENERATED ALWAYS AS (x) STORED`, Databricks drops the STORED, the neutral
    dialect and DuckDB write only `AS x`, and T-SQL writes `AS x` and drops
    the column's TYPE with it -- a computed column has no declared type there.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "CREATE TABLE t (zzcol INT GENERATED ALWAYS AS (zzexpr) STORED)",
            read="postgres",
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return "", False
    inside = text[text.index("(") + 1 : text.rindex(")")]
    kept = " INT" in inside or " INTEGER" in inside
    spelling = inside.split("zzcol", 1)[1]
    for word in (" INTEGER", " INT"):
        if spelling.startswith(word):
            spelling = spelling[len(word) :]
            break
    return spelling.strip().replace("zzexpr", "{expr}"), kept


def identity_written(dialect: str) -> bool:
    """Whether a GENERATED ... AS IDENTITY column keeps that spelling.

    T-SQL has no such constraint and rewrites every one of them into
    `IDENTITY(start, increment)`, which drops CYCLE and ON NULL with it.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY (CYCLE))"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return False
    return "GENERATED" in text and "CYCLE" in text


def identity_widens_type(dialect: str) -> bool:
    """Whether an identity column's type is widened to BIGINT.

    Databricks supports only BIGINT identity columns and the reference changes
    the declared type to match, which is a change to the tree rather than a
    spelling. It is reproduced rather than refused: the column still holds
    every value the narrower type could.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "CREATE TABLE t (x INT GENERATED ALWAYS AS IDENTITY)"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return False
    return "BIGINT" in text


def generated_expression_is_computed(dialect: str) -> bool:
    """Whether `GENERATED ALWAYS AS (x)` -- with no STORED -- is a COMPUTED
    column here.

    Databricks reads it that way; everyone else records it as an identity
    carrying an expression, odd as that reads. One statement, two nodes, and
    the dialect decides which.
    """
    import sqlglot
    from sqlglot import exp

    try:
        tree = sqlglot.parse_one(
            "CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2))", read=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return any(True for _ in tree.find_all(exp.ComputedColumnConstraint))


def index_on_word(dialect: str) -> str:
    """The word between an index's name and the table it is on.

    Databricks says `ON TABLE t`; everyone else says `ON t`.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE INDEX zzi ON zzt(zzc)").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return "ON"
    head, _, tail = text.partition("zzi ")
    del head
    return tail.split("zzt")[0].strip()


def string_class_sql(dialect: str) -> dict:
    """How each kind of quoted string is written, and whether its body is
    escaped on the way.

    Five classes the tokenizer already tells apart -- a raw string, a byte
    string, a unicode string, a hex string -- and every dialect spells them
    differently: PostgreSQL writes `x'1F'` where T-SQL writes `0x1F` and
    DuckDB writes `UNHEX('1F')`. An empty template means this dialect writes
    the value in a way that LOSES it, and the port refuses rather than
    following.

    The body is substituted verbatim into the template, so whether it needs a
    string's own escaping is asked separately -- a template cannot do it.
    """
    from sqlglot import exp

    out = {}
    for cls in ("RawString", "ByteString", "UnicodeString", "HexString", "BitString"):
        try:
            plain = getattr(exp, cls)(this="zzbody").sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        if "zzbody" not in plain:
            continue
        template = plain.replace("zzbody", "{body}")
        # Does a quote in the body come back escaped the way a plain string
        # literal's would?
        quoted = getattr(exp, cls)(this="a'b").sql(dialect=dialect or None)
        literal = exp.Literal.string("a'b").sql(dialect=dialect or None)
        body = literal[1:-1]
        escapes = "true" if template.replace("{body}", body) == quoted else "false"
        # A byte string goes further: a control character in the body is
        # written back as the two characters that spell it, so a tab comes
        # out as a backslash and a t.
        control = getattr(exp, cls)(this="a\tb").sql(dialect=dialect or None)
        if escapes == "true" and "\\t" in control:
            escapes = "byte"
        out[cls] = template + "\t" + escapes
    return out


def offset_rows_word(dialect: str) -> str:
    """The word T-SQL writes after an OFFSET count, and nobody else does.

    `OFFSET 2 ROWS` and `OFFSET 2` are the same tree; only the spelling
    differs, so the word is read off a rendering rather than kept on the node.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("SELECT * FROM t ORDER BY 1 OFFSET 2").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return ""
    return text.split("OFFSET 2", 1)[1].strip()


def interval_unit_aliases(dialect: str) -> dict:
    """The interval unit spellings the reference NORMALISES, and to what.

    `INTERVAL '500 us'` records MICROSECOND, not US. There is a 92-entry map
    behind it, but not every entry takes effect here -- `usec` and `hrs` are
    in it and come back unchanged -- so the map is not transcribed. Each key
    is RUN and only the ones that actually change are kept.
    """
    import sqlglot
    from sqlglot.dialects.dialect import Dialect

    D = Dialect.get_or_raise(dialect or None)
    out = {}
    for key in getattr(D, "DATE_PART_MAPPING", {}):
        if not key.isalpha():
            continue
        try:
            node = sqlglot.parse_one(
                "SELECT INTERVAL '1 %s'" % key, read=dialect or None
            ).selects[0]
        except Exception:  # noqa: BLE001
            continue
        unit = node.args.get("unit")
        if unit is None:
            continue
        if unit.name != key.upper():
            out[key.upper()] = unit.name
    return out


def table_hints_written(dialect: str) -> bool:
    """Whether a table's locking hints survive being written.

    `WITH (NOLOCK)` is T-SQL's; every other dialect here DROPS it. The hint is
    advisory rather than part of what the query returns, and dropping one only
    ever makes the read stricter -- which is why the port follows the
    reference here rather than refusing as it does where a guarantee is lost.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("SELECT x FROM a WITH (NOLOCK)", read="tsql").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return "NOLOCK" in text


def transaction_conventions(dialect: str) -> tuple[str, bool, bool]:
    """The word a transaction statement writes, and which NAMES survive it.

    Two different names, and the dialects disagree about them in opposite
    directions. A SAVEPOINT name says where to roll back to, and T-SQL drops
    it -- `ROLLBACK TO b` becomes `ROLLBACK TRANSACTION`, which rolls back
    everything rather than to the savepoint. A TRANSACTION name is what T-SQL
    alone keeps, and everyone else writes a bare `BEGIN` without it. Both are a
    different action from what was written, so each is refused where it is
    dropped -- and asked for separately, because one answer cannot serve both.
    """
    import sqlglot

    try:
        plain = sqlglot.parse_one("BEGIN").sql(dialect=dialect or None)
        savepoint = sqlglot.parse_one("ROLLBACK TO zzsave").sql(dialect=dialect or None)
        named = sqlglot.parse_one("BEGIN TRANSACTION zzname", read="tsql").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return "", True, True
    word = plain.replace("BEGIN", "").strip()
    return word, "zzsave" in savepoint, "zzname" in named


def bare_begin_is_a_transaction(dialect: str) -> bool:
    """Whether a bare BEGIN opens a TRANSACTION here.

    T-SQL's BEGIN opens a BLOCK -- `BEGIN ... END` -- and it takes the word
    TRANSACTION to mean the other thing. The reference gives up on the block
    form and keeps the raw text, which is not a tree this port builds.
    """
    import sqlglot

    try:
        return type(sqlglot.parse_one("BEGIN", read=dialect or None)).__name__ == "Transaction"
    except Exception:  # noqa: BLE001
        return False


def set_item_kind_written(dialect: str) -> dict:
    """Whether the SCOPE word of a SET survives, per word.

    `SET GLOBAL x = 1` says which scope the setting belongs to, and T-SQL --
    which has no such word -- drops it. A global setting applied to a session
    is a different effect, so the port refuses rather than following.
    """
    from sqlglot import exp

    out = {}
    for word in ("GLOBAL", "SESSION", "LOCAL", "VARIABLE"):
        # Built rather than parsed: `SET VARIABLE zzname = 1` read in a
        # dialect without the word puts VARIABLE in the NAME, and the word
        # then survives for a reason that has nothing to do with the scope.
        node = exp.Set(
            expressions=[
                exp.SetItem(
                    this=exp.EQ(
                        this=exp.column("zzname"), expression=exp.Literal.number(1)
                    ),
                    kind=word,
                )
            ],
            unset=False,
            tag=False,
        )
        try:
            text = node.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001
            out[word] = False
            continue
        out[word] = word in text.upper()
    return out


def set_variable_separator(dialect: str) -> str:
    """What sits between a VARIABLE and its value.

    T-SQL writes nothing between a setting and its value -- `SET XACT_ABORT
    ON` -- and an equals sign between a variable and its value, which is a
    different statement wearing the same word.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("SET @zzname = 'zzvalue'", read="tsql").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return " = "
    head, _, tail = text.partition("zzname")
    del head
    return tail.split("'zzvalue'")[0]


def set_without_a_sign(dialect: str) -> bool:
    """Whether a SET may be written with no `=` between name and value.

    `SET XACT_ABORT ON` is T-SQL's; everywhere else the reference gives up on
    it and keeps the raw text. Reading the form in dialects that have no such
    statement let the port read `SET@0B` as a setting and write back SQL it
    could not read.
    """
    import sqlglot

    try:
        return type(
            sqlglot.parse_one("SET zzname zzvalue", read=dialect or None)
        ).__name__ == "Set"
    except Exception:  # noqa: BLE001
        return False


def partition_sql(dialect: str) -> str:
    """How a table's PARTITION clause is written, with {members} in it.

    `PARTITION(ds)` almost everywhere; T-SQL wraps it as `WITH (PARTITIONS(ds))`.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "INSERT OVERWRITE TABLE t PARTITION(zzmember) SELECT 1"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return "PARTITION({members})"
    head, _, tail = text.partition("TABLE t ")
    del head
    return tail.split(" SELECT")[0].replace("zzmember", "{members}")


def alter_set_conventions(dialect: str) -> tuple[bool, bool]:
    """Whether an `ALTER TABLE ... SET` keeps what it sets, and whether a list
    of settings is parenthesised.

    Only PostgreSQL writes the option words -- LOGGED, TABLESPACE, ACCESS
    METHOD; everywhere else the reference writes a bare `ALTER TABLE t SET`,
    which sets nothing at all. The port refuses rather than writing that.
    """
    import sqlglot

    try:
        option = sqlglot.parse_one(
            "ALTER TABLE t SET LOGGED", read="postgres"
        ).sql(dialect=dialect or None)
        listed = sqlglot.parse_one(
            "ALTER TABLE t SET (zzname = 5)", read="postgres"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return False, False
    return "LOGGED" in option, "(zzname" in listed


def alter_set_list_is_settings(dialect: str) -> bool:
    """Whether `ALTER TABLE t SET (k = v)` is a list of SETTINGS here.

    T-SQL reads the same words as table PROPERTIES, each with a class of its
    own -- `SYSTEM_VERSIONING=OFF` is a property rather than an equality --
    so the port refuses there rather than building the wrong node.
    """
    import sqlglot
    from sqlglot import exp

    try:
        tree = sqlglot.parse_one(
            "ALTER TABLE t SET (zzname = 5)", read=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return not any(True for _ in tree.find_all(exp.Properties))


def key_constraint_options(dialect: str) -> dict:
    """The words that may follow a REFERENCES or a key, and what may follow
    each of them.

    `DEFERRABLE` stands alone; `INITIALLY` takes DEFERRED or IMMEDIATE; `NOT`
    takes ENFORCED. Read off the reference's own table rather than
    transcribed, because a word missing from it is a word the reference
    refuses.
    """
    from sqlglot.dialects.dialect import Dialect

    out = {}
    P = Dialect.get_or_raise(dialect or None).parser_class
    for word, follows in getattr(P, "KEY_CONSTRAINT_OPTIONS", {}).items():
        out[word.upper()] = sorted(f.upper() for f in (follows or ()))
    return out


def strips_ts_or_ds(dialect: str) -> list:
    """Which calls drop a formatless TS_OR_DS_TO_DATE from their argument.

    A call that reads a date out of whatever it is given has no use for a cast
    saying so, and some dialects take it back off: T-SQL writes YEAR(x) where
    the tree says YEAR(CAST(x AS DATE)).

    Two dialects reach that by different routes -- one lists the classes, the
    other rewrites each of them -- and only the first is readable as data. So
    it is PROBED instead: each candidate is written over the cast and over the
    bare column, and the ones that come out the same take it off.
    """
    from sqlglot import exp

    shapes = (
        ("this",),
        ("this", "expression", "unit"),
    )
    out = []
    for name in (
        "Year",
        "Month",
        "Day",
        "LastDay",
        "Quarter",
        "Week",
        "DayOfWeek",
        "DayOfMonth",
        "DayOfYear",
        "WeekOfYear",
        "DateDiff",
        "DateAdd",
        "DateSub",
    ):
        cls = getattr(exp, name, None)
        if cls is None:
            continue
        for shape in shapes:
            extra = {}
            if "expression" in shape:
                extra["expression"] = exp.column("y")
            if "unit" in shape:
                extra["unit"] = exp.var("DAY")
            try:
                wrapped = cls(this=exp.TsOrDsToDate(this=exp.column("x")), **extra).sql(
                    dialect=dialect or None
                )
                bare = cls(this=exp.column("x"), **extra).sql(dialect=dialect or None)
            except Exception:
                continue
            if wrapped == bare:
                out.append(name)
                break
    return out


def json_extract_twice(dialect: str) -> dict:
    """The spelling a dialect uses when it writes the value TWICE.

    T-SQL has no one call that reads both an object and a scalar out of JSON,
    so it asks both and takes whichever is not null:
    `ISNULL(JSON_QUERY(x, p), JSON_VALUE(x, p))`. That is one node written as
    two calls -- a spelling rather than a rewrite -- and the template it makes
    is measured here rather than written down.
    """
    from sqlglot import exp

    out = {}
    for name in ("JSONExtract", "JSONExtractScalar"):
        cls = getattr(exp, name, None)
        if cls is None:
            continue
        node = cls(
            this=exp.column("ZZTHISZZ"),
            expression=exp.JSONPath(
                expressions=[exp.JSONPathRoot(), exp.JSONPathKey(this="ZZKEYZZ")]
            ),
        )
        try:
            text = node.sql(dialect=dialect or None)
        except Exception:
            continue
        # Twice, and only twice: one appearance is the ordinary form and this
        # template has nothing to say about it.
        if text.count("ZZTHISZZ") != 2 or text.count("ZZKEYZZ") != 2:
            continue
        out[name] = text.replace("ZZTHISZZ", "{this}").replace(
            "'$.ZZKEYZZ'", "{path}"
        ).replace("ZZKEYZZ", "{key}")
    return out


def regexp_flag_args(dialect: str) -> dict:
    """Which argument of each regexp call holds the FLAGS.

    A flag says what the match does -- every occurrence, ignore case -- and the
    reference names the slot differently in each class: `modifiers` on a
    replacement, `parameters` on an extraction, `flag` on a test. The names are
    the class definitions rather than a guess, so they are read off rather than
    probed; what each DIALECT then does with them is probed separately.
    """
    from sqlglot import exp

    named = ("modifiers", "parameters", "flag")
    out = {}
    for name in (
        "RegexpReplace",
        "RegexpExtract",
        "RegexpExtractAll",
        "RegexpLike",
        "RegexpCount",
    ):
        cls = getattr(exp, name, None)
        if cls is None:
            continue
        for key in cls.arg_types:
            if key in named:
                out[name] = key
                break
    return out


def regexp_flags(dialect: str) -> tuple:
    """Which flags a REGEXP_REPLACE may carry here, and in what form.

    Asked by writing a replacement carrying every flag anyone uses and seeing
    which survive, then by writing one whose flags are a COLUMN and seeing
    whether they survive at all. DuckDB keeps "cimsg" and only from a string;
    Databricks writes none; everyone else writes whatever it is given.
    """
    from sqlglot import exp

    def probe(mods):
        node = exp.RegexpReplace(
            this=exp.column("a"),
            expression=exp.column("b"),
            replacement=exp.column("c"),
            modifiers=mods,
            single_replace=True,
        )
        try:
            return node.sql(dialect=dialect or None)
        except Exception:
            return ""

    every = "cimsgxz"
    literal = probe(exp.Literal.string(every))
    written = "'" in literal
    kept = ""
    if written:
        start = literal.index("'")
        kept = literal[start + 1 : literal.index("'", start + 1)]
    need_literal = written and "f" not in probe(exp.column("f"))
    # A dialect that keeps every flag it was offered filters none, and says so
    # with an empty set rather than with a list of everything anyone might
    # write -- the list would refuse a flag nobody has thought of yet.
    return ("" if kept == every else kept, written, need_literal)


def ignore_nulls_dropped(dialect: str) -> list:
    """Which calls lose their IGNORE NULLS here without the reference minding.

    DuckDB writes the words inside the call for the window functions that take
    them, drops them for the calls that ignore nulls anyway, and calls every
    other use unsupported. The first two are worth reproducing and the third is
    not, so the set is found by generating with unsupported raised: whatever
    comes back WITHOUT the words, and without complaint, is dropped on purpose.
    """
    import sqlglot
    from sqlglot import exp

    probes = [
        # Without the words too: a call the dialect READS as already ignoring
        # them carries the node whether or not they were written, and writing
        # them out again is what it calls unsupported.
        "SELECT ANY_VALUE(x)",
        "SELECT ANY_VALUE(x IGNORE NULLS)",
        "SELECT SUM(x IGNORE NULLS)",
        "SELECT MIN(x IGNORE NULLS)",
        "SELECT MAX(x IGNORE NULLS)",
        "SELECT COUNT(x IGNORE NULLS)",
        "SELECT AVG(x IGNORE NULLS)",
        "SELECT ARRAY_AGG(x IGNORE NULLS)",
        "SELECT FIRST(x IGNORE NULLS)",
    ]
    out = set()
    for probe in probes:
        try:
            node = sqlglot.parse_one(probe, read=dialect or None)
        except Exception:
            continue
        wrapped = node.find(exp.IgnoreNulls)
        if wrapped is None or wrapped.this is None:
            continue
        try:
            text = node.sql(dialect=dialect or None, unsupported_level=sqlglot.ErrorLevel.RAISE)
        except Exception:
            continue
        # Dropped means the words went and NOTHING else changed. A call the
        # dialect rewrites instead -- ARRAY_AGG into a FILTER, FIRST into
        # ANY_VALUE -- also comes back without them, and that is a different
        # tree rather than the same one spelled shorter.
        bare = node.copy()
        target = bare.find(exp.IgnoreNulls)
        target.replace(target.this)
        try:
            plain = bare.sql(dialect=dialect or None, unsupported_level=sqlglot.ErrorLevel.RAISE)
        except Exception:
            continue
        if text == plain and "IGNORE NULLS" not in text.upper():
            out.add(type(wrapped.this).__name__)
    return sorted(out)


def condition_coercion(dialect: str) -> str:
    """How a value that is not already a condition is made into one.

    T-SQL has no boolean type, so a bare column in a condition is compared
    against zero: `NOT c` is written `NOT c <> 0`. Everyone else takes the
    value as it stands, and says so with an empty template.
    """
    from sqlglot import exp

    try:
        text = exp.Not(this=exp.column("ZZVALZZ")).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ""
    inner = text[len("NOT ") :] if text.startswith("NOT ") else text
    if inner == "ZZVALZZ":
        return ""
    return inner.replace("ZZVALZZ", "{value}")


def split_part_backwards(dialect: str) -> str:
    """How a dialect that counts a DOTTED name from the other end opens the
    call it counts with.

    T-SQL has no SPLIT_PART and uses PARSENAME, which numbers the pieces right
    to left. What is measured is the text in front of the number; the number
    itself is worked out from the name.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("SELECT SPLIT_PART('zza.zzb.zzc', '.', 1)").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return ""
    if "SPLIT_PART" in text or "'zza.zzb.zzc'" not in text:
        return ""
    head, _, _ = text.partition("'zza.zzb.zzc'")
    return head[len("SELECT ") :] + "{this}, "


def file_format_sql(dialect: str) -> str:
    """How the storage format a table is written in is spelled.

    The format is a WORD rather than a value -- Databricks says `USING PARQUET`
    and the rest `FORMAT=PARQUET` -- so the template carries its name. The
    generic probe fills the slot with a column and the dialect writes the
    column's NAME, which is nothing when there is none, so this one asks with
    a var.
    """
    from sqlglot import exp

    try:
        text = exp.FileFormatProperty(this=exp.var("ZZFMTZZ")).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ""
    if "ZZFMTZZ" not in text:
        return ""
    return text.replace("ZZFMTZZ", "{name}")


def date_delta_is_an_operator(dialect: str) -> bool:
    """Whether a date shifted by an interval is written with an operator.

    PostgreSQL and DuckDB write `d + INTERVAL 1 DAY`; T-SQL and Databricks
    write DATEADD. Asked by rendering the shift and looking for the sign.
    """
    from sqlglot import exp

    node = exp.DateAdd(
        this=exp.column("zzd"),
        expression=exp.Interval(this=exp.Literal.string("1"), unit=exp.var("DAY")),
    )
    try:
        return node.sql(dialect=dialect or None).startswith("zzd +")
    except Exception:  # noqa: BLE001
        return False


def column_comment_written(dialect: str) -> bool:
    """Whether a COMMENT on a column survives being written.

    PostgreSQL and DuckDB have nowhere to say it in a CREATE and write nothing.
    Asked by rendering a column that carries one and looking for the word.
    """
    from sqlglot import exp

    node = exp.ColumnDef(
        this=exp.to_identifier("zzc"),
        kind=exp.DataType.build("INT"),
        constraints=[
            exp.ColumnConstraint(
                kind=exp.CommentColumnConstraint(this=exp.Literal.string("zzhi"))
            )
        ],
    )
    try:
        return "zzhi" in node.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return True


def comma_unnest_joins(dialect: str) -> bool:
    """Whether a comma join over an UNNEST becomes an explicit JOIN here.

    DuckDB writes `FROM t JOIN UNNEST(x) AS u(c) ON TRUE` where the statement
    said `FROM t, UNNEST(x) AS u(c)`: the comma form does not bind the
    unnested rows to the row they came from. Everyone else keeps the comma.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("SELECT * FROM t1, UNNEST(x) AS u(c)").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return " JOIN UNNEST" in text and " ON TRUE" in text


def within_group_inside(dialect: str) -> bool:
    """Whether the ordering is written INSIDE the call rather than after it.

    `LISTAGG(x, \',\') WITHIN GROUP (ORDER BY y)` keeps its own clause almost
    everywhere; DuckDB folds the ordering into the call. Probed with LISTAGG
    rather than a percentile because a percentile may be rewritten into a
    different function altogether, which is a third thing -- see
    within_group_percentile.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("LISTAGG(x, \',\') WITHIN GROUP (ORDER BY y)").sql(
            dialect=dialect or None
        )
    except Exception:
        return False
    return "WITHIN GROUP" not in text


def within_group_percentile(dialect: str) -> str:
    """What a PERCENTILE under a WITHIN GROUP becomes here.

    Three answers. "outside" keeps the clause where it was written. "inside"
    is DuckDB, which folds the ordering into the call AND moves the order key
    into the first argument, sliding the fraction right. An empty string is a
    dialect that writes a DIFFERENT function -- Databricks turns the pair into
    PERCENTILE_APPROX -- which is a rewrite rather than a spelling, and the
    port refuses those.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x)"
        ).sql(dialect=dialect or None)
    except Exception:
        return ""
    if "WITHIN GROUP" in text:
        return "outside"
    if "ORDER BY" in text:
        return "inside"
    return ""


def enum_type_sql(dialect: str) -> str:
    """How an ENUM type's members are written, with {members} standing in.

    PostgreSQL writes `ENUM (\'a\')` -- a space in front of the list, and the
    parentheses even when the list is empty. Everywhere else it is an ordinary
    parameterised type, `ENUM(\'a\')`, and vanishes to a bare name when empty.
    The difference is asked of the reference rather than written down, the same
    way every other spelling here is.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CAST(x AS ENUM('a'))", read=dialect or None).sql(
            dialect=dialect or None
        )
    except Exception:
        return ""
    start = text.find("ENUM")
    if start < 0:
        return ""
    return text[start:-1].replace("'a'", "{members}")


def with_data_written(dialect: str) -> bool:
    """Whether `WITH NO DATA` survives being written.

    It says whether the table is FILLED from the query or only shaped by it,
    which is the difference between a copy and an empty table. DuckDB and
    Databricks drop the words, so the port refuses rather than following.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("CREATE TABLE t AS SELECT 1 WITH NO DATA").sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return "NO DATA" in text.upper()


def identity_short_sql(dialect: str) -> str:
    """The short spelling of an identity column, with {start} and {increment}.

    T-SQL writes `IDENTITY(7, 9)` where everyone else writes the whole
    `GENERATED BY DEFAULT AS IDENTITY (START WITH 7 INCREMENT BY 9)`. The
    short form has nowhere to put CYCLE or ALWAYS, so a column that says one
    of those is refused rather than written without it.
    """
    from sqlglot import exp

    node = exp.ColumnDef(
        this=exp.to_identifier("zzc"),
        kind=exp.DataType.build("INT"),
        constraints=[
            exp.ColumnConstraint(
                kind=exp.GeneratedAsIdentityColumnConstraint(
                    this=False,
                    start=exp.Literal.number(7),
                    increment=exp.Literal.number(9),
                )
            )
        ],
    )
    try:
        text = node.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ""
    if "GENERATED" in text.upper():
        return ""
    head, _, tail = text.partition("zzc ")
    del head
    return tail.split(" ", 1)[-1].replace("7", "{start}").replace("9", "{increment}")


def auto_increment_sql(dialect: str) -> str:
    """How an auto-incrementing column is spelled.

    `AUTO_INCREMENT` in most, `IDENTITY` in T-SQL, the whole
    `GENERATED BY DEFAULT AS IDENTITY NOT NULL` in PostgreSQL -- and DuckDB
    writes nothing at all, which drops the numbering. The empty answer is a
    refusal rather than a spelling.
    """
    from sqlglot import exp

    node = exp.ColumnDef(
        this=exp.to_identifier("zzc"),
        kind=exp.DataType.build("INT"),
        constraints=[exp.ColumnConstraint(kind=exp.AutoIncrementColumnConstraint())],
    )
    try:
        text = node.sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ""
    head, _, tail = text.partition("zzc ")
    del head
    parts = tail.split(" ", 1)
    if len(parts) < 2:
        return ""
    return parts[1]


def values_table_wrapped(dialect: str) -> bool:
    """Whether a VALUES clause used as a TABLE is written in parentheses.

    `FROM (VALUES (1)) AS t(c)` almost everywhere; Databricks writes it bare.
    The tree is the same either way -- the reference normalises the source's
    parentheses away -- so the wrapping is the writer's alone.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "SELECT c FROM (VALUES (1)) AS t(c)", read="postgres"
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return True
    return "(VALUES" in text


def merge_without_target(dialect: str) -> bool:
    """Whether a MERGE drops the target's name from the columns it assigns.

    PostgreSQL and Trino write `UPDATE SET a = y.b` for a branch written as
    `SET x.a = y.b`, because the target is the only thing that side can name.
    The tree keeps the qualifier; only the spelling drops it, and only where
    it names the TARGET -- the right-hand side keeps its own.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "MERGE INTO x USING (SELECT id) AS y ON a = b "
            "WHEN MATCHED THEN UPDATE SET x.a = y.b",
            read="postgres",
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return False
    return "SET x.a" not in text


def identifier_normalization(dialect: str) -> tuple[str, str]:
    """The case a name is COMPARED in, quoted and unquoted.

    Only comparison, not spelling: the MERGE rule above asks whether a column's
    qualifier is the target, and `X.a` names `x` in a dialect that folds and
    something else in one that does not.
    """
    from sqlglot import exp
    from sqlglot.dialects.dialect import Dialect

    D = Dialect.get_or_raise(dialect or None)

    def fold(quoted: bool) -> str:
        name = D.normalize_identifier(exp.to_identifier("AbC", quoted=quoted)).name
        if name == "abc":
            return "lower"
        if name == "ABC":
            return "upper"
        return ""

    return fold(False), fold(True)


def rename_target(dialect: str) -> str:
    """How much of a qualified name an ALTER ... RENAME TO writes.

    Three answers. DuckDB and PostgreSQL write only the last part -- the new
    table lives where the old one did, so the qualifier would be noise.
    Databricks and the neutral dialect keep it whole. T-SQL has no such
    statement at all and rewrites the whole thing into `EXEC sp_rename`, which
    is a transformation this port does not do.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one(
            "ALTER TABLE db.t1 RENAME TO db.t2", read=dialect or None
        ).sql(dialect=dialect or None)
    except Exception:  # noqa: BLE001
        return ""
    if not text.upper().startswith("ALTER"):
        return ""
    if "db.t2" in text:
        return "whole"
    return "name"


def json_operators_at_bitwise(dialect: str) -> dict:
    """Which operators this dialect reads at the BITWISE tier, and as what.

    PostgreSQL takes the JSON operators out of the accessor tier and reads
    them level with `||` -- so `1 + x #> 'y'` is `(1 + x) #> 'y'` there and
    `1 + (x #> 'y')` everywhere else. DuckDB moves the ARROWS and leaves the
    rest behind, which is three separate per-dialect facts and exactly the
    sort of thing to measure rather than transcribe.

    The probe is the asymmetry itself: parse `1 + x <op> 'y'` and look at
    what ended up on top. The operator's own class means it swallowed the
    sum, which is the bitwise tier; an Add means it bound tighter.
    """
    import sqlglot

    out = {}
    for token, text in (
        ("HASH_ARROW", "#>"),
        ("DHASH_ARROW", "#>>"),
        ("PLACEHOLDER", "?"),
    ):
        try:
            top = sqlglot.parse_one(f"1 + x {text} 'y'", read=dialect or None)
        except Exception:  # noqa: BLE001
            continue
        if type(top).__name__ != "Add":
            out[token] = (type(top).__name__, text)
    return out


def truncates_catalog(dialect: str) -> bool:
    """Whether a three-part name is written with only two of them.

    T-SQL does, dropping the catalog -- which names a different object, so the
    port refuses rather than writing it. The rule belongs to the DIALECT's
    naming rather than to any one statement: a DROP and a CREATE lose the
    part the same way, and the flag was named for the first place it was
    found.
    """
    import sqlglot

    try:
        text = sqlglot.parse_one("DROP VIEW a.b.c", read=dialect or None).sql(
            dialect=dialect or None
        )
    except Exception:  # noqa: BLE001
        return False
    return "a.b.c" not in text


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
    the separator but still inside the call, or unfolded into a WITHIN GROUP
    again. Anything else is refused rather than written in the wrong place.
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
    # PostgreSQL and DuckDB put it after the separator and inside the call:
    # `STRING_AGG(x, ',' ORDER BY y DESC)`.
    if text.rstrip().endswith(")") and "ORDER BY y DESC" in text:
        return "after_separator"
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


def convert_builds_convert(dialect: str) -> bool:
    """Whether CONVERT(type, value) builds a Convert in this dialect.

    T-SQL's CONVERT names the type FIRST and keeps the call as a Convert;
    everywhere else the same word is a CAST written another way, and the
    reference builds a Cast whose arguments come out in an order nothing here
    would guess. The override lives on a parser class, which is not a thing
    the port can read -- so it is asked rather than transcribed.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one("SELECT CONVERT(INT, x)", read=dialect or None).selects[0]
    except Exception:  # noqa: BLE001 -- not a form this dialect has
        return False
    return type(node).__name__ == "Convert"


def unary_ops(dialect: str, P, exp) -> dict:
    """Which token opens a PREFIX operator here, and what it builds.

    The base table is five entries, and a dialect adds to it: PostgreSQL reads
    `~` as its binary regexp operator, so its prefix form arrives under the
    RLIKE token rather than TILDE, and a port that matched on TILDE alone read
    `~x` as nothing at all.

    Read by RUNNING the reference rather than by transcribing the lambdas: the
    operator's own spelling comes from the tokenizer, and the class from what
    parsing `<op> x` actually builds. A no-op -- unary plus -- records the
    empty string, since what it yields is the operand itself.
    """
    import sqlglot
    from sqlglot.dialects.dialect import Dialect

    T = type(Dialect.get_or_raise(dialect or None)).tokenizer_class
    spellings: dict = {}
    for table in (getattr(T, "SINGLE_TOKENS", {}), getattr(T, "KEYWORDS", {})):
        for text, token in table.items():
            spellings.setdefault(token, text)

    out = {}
    for token in P.UNARY_PARSERS:
        text = spellings.get(token)
        if not text:
            continue
        try:
            node = sqlglot.parse_one(f"SELECT {text} x", read=dialect or None).selects[0]
        except Exception:  # noqa: BLE001 -- not a prefix this dialect reads
            continue
        name = type(node).__name__
        out[token.name] = "" if name == "Column" else name
    return out


def execute_builds_execute(dialect: str) -> bool:
    """Whether EXEC names a procedure to run in this dialect.

    Only T-SQL reads it; everywhere else the word opens a statement the
    reference keeps as raw text. The mapping lives in a statement-parser table
    keyed by token, so it is asked rather than transcribed.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one("EXECUTE p", read=dialect or None)
    except Exception:  # noqa: BLE001 -- not a statement this dialect has
        return False
    return type(node).__name__ in ("Execute", "ExecuteSql")


def prefix_calls(dialect: str, P) -> dict:
    """Punctuation that names a FUNCTION written in front of its operand.

    DuckDB's `@x` is ABS(x). It lives among the no-paren function names rather
    than among the prefix operators, and it takes a whole arithmetic
    expression: `@col + 1` is ABS(col + 1), not ABS(col) + 1.

    Keyed by the characters rather than by a token type, because the same
    token type is a parameter marker here as well -- `$1` and `@x` are both
    PARAMETER in DuckDB and only one of them is this.
    """
    import sqlglot

    out = {}
    for key in P.NO_PAREN_FUNCTION_PARSERS:
        if key.isalnum() or "_" in key:
            continue
        try:
            node = sqlglot.parse_one(f"SELECT {key} x", read=dialect or None).selects[0]
        except Exception:  # noqa: BLE001 -- not a prefix this dialect reads
            continue
        name = type(node).__name__
        if name not in ("Column", "Parameter", "Placeholder"):
            out[key] = name
    return out


def inverse_format_classes(dialect: str, exp, classes) -> list:
    """Classes whose FORMAT argument is written back in the dialect's spelling.

    The tree stores `%Y-%m-%d` and PostgreSQL writes `YYYY-MM-DD`, but only
    for some of the classes that carry a format: TO_CHAR translates and
    STR_TO_UNIX does not. So each is rendered with a format the mapping moves,
    and kept only where the output actually shows the moved spelling.
    """
    from sqlglot.dialects.dialect import Dialect
    from sqlglot.time import format_time

    D = Dialect.get_or_raise(dialect or None)
    inverse = getattr(D, "INVERSE_TIME_MAPPING", None) or {}
    if not inverse:
        return []
    probe = "%Y-%m-%d"
    want = format_time(probe, inverse)
    default_stored = format_time(
        str(getattr(D, "TIME_FORMAT", "") or "").strip("'"),
        getattr(D, "TIME_MAPPING", None) or {},
    )
    if not want or want == probe:
        return []

    out = []
    for name in sorted(classes):
        cls = getattr(exp, name, None)
        arg_types = getattr(cls, "arg_types", None)
        if not isinstance(cls, type) or not arg_types or "format" not in arg_types:
            continue
        try:
            node = cls(this=exp.column("ZZTHISZZ"), format=exp.Literal.string(probe))
            text = Dialect.get_or_raise(dialect or None).generator().sql(node)
        except Exception:  # noqa: BLE001 -- a class this probe cannot build
            continue
        # A format that is the dialect's OWN DEFAULT is written as nothing at
        # all: Databricks spells `FROM_UNIXTIME(x)` for the format it would
        # otherwise put down in full.
        if default_stored:
            try:
                bare = Dialect.get_or_raise(dialect or None).generator().sql(
                    cls(this=exp.column("ZZTHISZZ"),
                        format=exp.Literal.string(default_stored))
                )
            except Exception:  # noqa: BLE001
                bare = ""
            if bare and "'" not in bare:
                out.append((name, "default-dropped"))
                continue
        if want in text and probe not in text:
            out.append((name, "inverse"))
        elif probe in text:
            out.append((name, "verbatim"))
        else:
            # A spelling that is neither the one stored nor the one this
            # mapping gives: the dialect writes that class through a table of
            # its own -- Databricks spells a TO_DATE format `yyyy-M-d` and a
            # DATE_FORMAT one `yyyy-MM-dd`, from the same stored `%Y-%m-%d`.
            # Recorded as neither, and the writer declines rather than put
            # down a format that says something else.
            out.append((name, "other"))
    return out


def end_commits(dialect: str) -> bool:
    """Whether a bare END ends the transaction in this dialect.

    PostgreSQL reads END as COMMIT; everywhere else the word is a name or the
    close of a block. The mapping lives in a statement-parser table keyed by
    token, which the port has no way to read, so it is asked instead.
    """
    import sqlglot

    try:
        node = sqlglot.parse_one("END", read=dialect or None)
    except Exception:  # noqa: BLE001 -- not a statement this dialect has
        return False
    return type(node).__name__ == "Commit"


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
            listed = False
            if sorted(node.args) == ["expression", "this"]:
                left, right = node.args["this"], node.args["expression"]
            elif sorted(node.args) == ["expressions", "this"]:
                # One operand held as a LIST of one: PostgreSQL's `x @@ y` is
                # a MatchAgainst of y over [x]. Still a binary as far as
                # anyone writing SQL is concerned, and the list is the
                # reference's own shape rather than a second operand.
                held = node.args["expressions"]
                if not isinstance(held, list) or len(held) != 1:
                    continue
                left, right = node.args["this"], held[0]
                listed = True
            else:
                continue
            names = (getattr(left, "name", None), getattr(right, "name", None))
            # Some operators put the operands the other way round:
            # PostgreSQL's `x @@ y` is a MatchAgainst of y over x. The order
            # is recorded rather than assumed, and an operator whose operands
            # are neither way round is not a plain binary at all.
            if names == ("a", "b"):
                swapped = False
            elif names == ("b", "a"):
                swapped = True
            else:
                continue
            # The class AND how this dialect writes it back. The two are not
            # the same fact: `~*` reads as RegexpILike here and PostgreSQL
            # writes it `~*` again, but another dialect could spell the same
            # node differently.
            written = node.sql(dialect=dialect or None)
            head, sep, tail = written.partition("a ")
            op = tail.rpartition(" b")[0] if sep else ""
            out[token.name] = {
                "class": type(node).__name__,
                "op": op,
                "swapped": swapped,
                "listed": listed,
            }
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


CAST_PROBE_TYPES = (
    "DOUBLE",
    "DECIMAL",
    "REAL",
    "FLOAT",
    "BOOLEAN",
    "TEXT",
    "BLOB",
    "DATE",
    "TIMESTAMP",
    "TIMESTAMPTZ",
    "TIMESTAMPNTZ",
    "DATETIME",
    "BIGINT",
    "INT",
    "VARIANT",
)


def _cast_probe(builder, plain, base_sql, i, probe, dialect, name, sensitive, keyof=None,
                vanished=None, cast_type=None, types=None):
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
        _record_type(types, name, len(plain), i, keyof, cast_type)
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
        _record_type(types, name, len(plain), i, keyof, cast_type)
        return
    if got.count(probe_sql) != 1:
        return
    if got != base_sql.replace(token, probe_sql, 1):
        _record(sensitive, name, len(plain), i, keyof)
        _record_type(types, name, len(plain), i, keyof, cast_type)


def _record_type(types, name, arity, index, keyof, cast_type):
    """Record WHICH cast target moved the rendering, beside the key it moved.

    A slot is not sensitive to casting as such: it is sensitive to being cast
    to particular types. DuckDB wraps a non-text argument to UPPER in a cast to
    TEXT, and leaves one that is already TEXT alone.
    """
    if types is None or cast_type is None:
        return
    key = (keyof or {}).get(index)
    if key is None:
        return
    bucket = types.setdefault(name, {}).setdefault(arity, {}).setdefault(key, [])
    if cast_type not in bucket:
        bucket.append(cast_type)


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
    cast_types: dict[str, dict[int, dict[str, list[str]]]] = {}
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
                # Every type worth asking about, not just one. A slot that
                # moves for a DOUBLE need not move for a TEXT, and refusing
                # both because one was observed turned away
                # `UPPER(CAST(x AS TEXT))`, which renders exactly as written.
                for target in CAST_PROBE_TYPES:
                    _cast_probe(
                        builder, plain, base_sql, i,
                        exp.cast(exp.column(f"__probeArg{chr(65 + i)}"), target),
                        dialect, cls_name, sensitive, keyof,
                        cast_type=target, types=cast_types,
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

    return tidy(sensitive), tidy(zero_sensitive), tidy(drops_zero), cast_types


def class_candidates(P, exp, dialect):
    """Every class a one-argument call in this dialect can produce.

    The space of NESTED calls a caller could actually write, read off the
    reference's own catalogue rather than listed. Two probes want it: the one
    that finds which positions branch on a class, and the one that describes a
    builder at all -- which has to know that a class change at such a position
    is already accounted for.
    """
    candidates: dict[str, object] = {}
    for builder in P.FUNCTIONS.values():
        try:
            node = call_builder(builder, [exp.column("__inner")], dialect)
        except Exception:  # noqa: BLE001 -- not a one-argument name
            continue
        if isinstance(node, exp.Expr):
            candidates.setdefault(type(node).__name__, node)
    return candidates


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
    candidates = class_candidates(P, exp, dialect)

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


def projection_shape(dialect: str, sql: str) -> str:
    """The class of the first projection this dialect reads a statement into.

    Two shapes below are the same TEXT meaning different things in different
    dialects -- `SELECT a = 1` is an alias in T-SQL and a comparison
    everywhere else -- so which it is has to be asked rather than assumed.
    """
    import logging

    import sqlglot

    was = logging.root.manager.disable
    logging.disable(logging.CRITICAL)
    try:
        tree = sqlglot.parse_one(sql, read=dialect or None)
    except Exception:  # noqa: BLE001 -- not a shape this dialect reads at all
        return ""
    finally:
        logging.disable(was)
    held = tree.args.get("expressions") or []
    return type(held[0]).__name__ if held else ""


def keeps_unnamed_wrapped(dialect: str, funcs) -> list:
    """Names whose WRAPPED slot keeps the node when the argument has no name.

    A wrap takes the argument's NAME -- DATEADD records unit=Var(DAY) -- so an
    argument with no name to take is usually one the reference builds some
    other way, and the port refuses rather than guess. But some builders
    simply keep whatever they were handed: PostgreSQL's DATE_BIN puts a
    subquery straight into its unit slot. Which do is asked rather than
    assumed.
    """
    import logging

    import sqlglot
    from sqlglot import expressions as e

    was = logging.root.manager.disable
    logging.disable(logging.CRITICAL)
    out = []
    try:
        for name, (_cls, spec) in funcs.items():
            wrapped = [how["index"] for _, how in spec if "wrap" in how]
            if not wrapped:
                continue
            width = max(wrapped) + 1
            args = ["zza"] * width
            for i in wrapped:
                args[i] = "(SELECT 1)"
            try:
                tree = sqlglot.parse_one(
                    f"SELECT {name}({', '.join(args)})", read=dialect or None
                )
            except Exception:  # noqa: BLE001
                continue
            held = tree.args.get("expressions") or []
            if not held or type(held[0]).__name__ != _cls:
                continue
            # Every wrapped slot holds the SUBQUERY itself rather than a name
            # made from it.
            keys = [key for key, how in spec if "wrap" in how]
            if all(
                isinstance(held[0].args.get(k), e.Subquery) for k in keys
            ):
                out.append(name)
    finally:
        logging.disable(was)
    return out


def user_defined_type_is_identifier(dialect: str) -> bool:
    """Whether a user-defined type's NAME is wrapped in an Identifier.

    PostgreSQL wraps it and the rest keep the word as it stands, which is a
    difference the dump shows and nothing else does.
    """
    import logging

    import sqlglot
    from sqlglot import expressions as e

    was = logging.root.manager.disable
    logging.disable(logging.CRITICAL)
    try:
        tree = sqlglot.parse_one("CAST(zzx AS zzudt)", read=dialect or None)
    except Exception:  # noqa: BLE001
        return False
    finally:
        logging.disable(was)
    to = tree.args.get("to")
    return isinstance(to, e.DataType) and isinstance(to.args.get("kind"), e.Identifier)


def create_words(dialect: str):
    """The words that may stand between CREATE and what it creates.

    Three things such a word can be, and which is which is asked rather than
    listed: a bare PROPERTY (`CREATE MATERIALIZED VIEW` carries a
    MaterializedProperty), a KIND in its own right (`CREATE DATABASE`), or
    nothing at all. The candidates are the dialect's own keywords, so a word
    one dialect knows and another does not sorts itself out.

    The property's class is read off the tree rather than made from the word:
    Databricks' STREAMING becomes a StreamingTableProperty, which no rule
    over the spelling would have produced.
    """
    import logging

    import sqlglot
    from sqlglot.dialects.dialect import Dialect

    from sqlglot import expressions as e

    tokenizer = Dialect.get_or_raise(dialect or None).tokenizer_class
    # Every word the tokenizer knows, including the FIRST word of a phrase --
    # MATERIALIZED reaches the tokenizer only inside `MATERIALIZED VIEW`, and
    # a list of single-word keys missed it and three others like it. Beside
    # them, the words the reference's own property CLASSES are named for, so
    # a property whose word the tokenizer spells some other way is still put
    # to the test.
    words = {
        k.upper().split(" ")[0]
        for k in tokenizer.KEYWORDS
        if k.split(" ")[0].isalpha()
    }
    for n in dir(e):
        if not n.endswith("Property") or n == "Property":
            continue
        # Every CamelCase prefix of the class name, not just the whole of it:
        # Databricks' `CREATE STREAMING TABLE` builds a StreamingTableProperty,
        # and STREAMINGTABLE is not a word anyone writes.
        stem = n[: -len("Property")]
        for m in re.finditer(r"[A-Z][a-z0-9]*", stem):
            words.add(stem[: m.end()].upper())
            words.add(m.group(0).upper())
    words = sorted(words)
    properties: dict[str, str] = {}
    kinds: set[str] = set()
    was = logging.root.manager.disable
    logging.disable(logging.CRITICAL)
    try:
        for word in words:
            for kind in ("TABLE", "VIEW"):
                try:
                    tree = sqlglot.parse_one(
                        f"CREATE {word} {kind} zzt AS SELECT 1", read=dialect or None
                    )
                except Exception:  # noqa: BLE001 -- not a word that stands here
                    continue
                if tree.key != "create":
                    continue
                built = (tree.args.get("kind") or "").upper()
                if built != kind:
                    continue
                props = tree.args.get("properties")
                held = list(props.expressions) if props else []
                if len(held) == 1 and not held[0].args:
                    properties[word] = type(held[0]).__name__
                break
        # A word that is not a modifier at all but a KIND of its own: what
        # `CREATE DATABASE x` creates is a database, and the word is the kind
        # rather than something in front of one.
        for word in words:
            try:
                tree = sqlglot.parse_one(f"CREATE {word} zzname", read=dialect or None)
            except Exception:  # noqa: BLE001 -- not a kind this dialect creates
                continue
            if tree.key == "create" and (tree.args.get("kind") or "").upper() == word:
                kinds.add(word)
        # And the words after CREATE OR, each of which sets a FLAG rather than
        # carrying a property: T-SQL's `OR ALTER` is the reference's `replace`,
        # and Databricks' `OR REFRESH` its `refresh`.
        flags: dict[str, str] = {}
        plain = sqlglot.parse_one("CREATE VIEW zzv AS SELECT 1", read=dialect or None)
        for word in words:
            try:
                tree = sqlglot.parse_one(
                    f"CREATE OR {word} VIEW zzv AS SELECT 1", read=dialect or None
                )
            except Exception:  # noqa: BLE001
                continue
            if tree.key != "create":
                continue
            turned = [
                k
                for k, v in tree.args.items()
                if v is True and plain.args.get(k) is not True
            ]
            if len(turned) == 1:
                flags[word] = turned[0]
    finally:
        logging.disable(was)
    return properties, sorted(kinds), flags


def create_replace_words(dialect: str) -> dict:
    """How this dialect WRITES the flags `CREATE OR ...` turned on.

    Reading and writing are not the same word: T-SQL reads both OR REPLACE and
    OR ALTER, and writes OR ALTER. Asked by rendering a view with each flag set
    and reading back what stands between CREATE and VIEW.
    """
    import logging

    import sqlglot

    was = logging.root.manager.disable
    logging.disable(logging.CRITICAL)
    out = {}
    try:
        for flag in ("replace", "refresh"):
            tree = sqlglot.parse_one("CREATE VIEW zzv AS SELECT 1")
            tree.set(flag, True)
            try:
                text = tree.sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001
                continue
            head, sep, _ = text.partition(" VIEW ")
            if not sep or not head.startswith("CREATE "):
                continue
            words = head[len("CREATE ") :].strip()
            if words:
                out[flag] = words
    finally:
        logging.disable(was)
    return out


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
            # A node with NO arguments is a shape too, and the commonest
            # kind of one: a bare property or constraint whose whole meaning
            # is the word. Skipping those left every one of them without a
            # spelling -- MaterializedProperty, NotForReplication -- so the
            # parser could read a statement the writer then refused.
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
    # The storage format a table is written in. The corpus writes it only in
    # dialects that DROP it, so no shape was observed for the ones that keep
    # it -- and Databricks says `USING PARQUET` where the rest say
    # `FORMAT=PARQUET`.
    "FileFormatProperty": [(("this",), ())],
    # The EMPTY call. `ARRAY_CONSTRUCT_COMPACT()` becomes a filter over an
    # empty list, and no shape without arguments was observed for it.
    "ArrayConstructCompact": [((), ())],
    "StrPosition": [(("this", "substr"), ())],
    "Extract": [(("this", "expression"), ())],
    # The unit a rounding rounds TO. The corpus never writes one, so no shape
    # was observed for it -- and the port reads the grammar that produces it.
    # Hive's reducer clauses. The corpus writes them in one dialect apiece,
    # and the port reads them in every dialect that has the words.
    # Every combination of what may follow a LIMIT's count. The corpus writes
    # one of them; the port reads all three words.
    "LimitOptions": [
        ((), (("percent", p), ("rows", r), ("with_ties", w)))
        for p in (True, False)
        for r in (True, False)
        for w in (True, False)
        if p or r or w
    ],
    # T-SQL's JSON_ARRAYAGG reads how nulls are handled; the corpus writes
    # only the plain form.
    "JSONArrayAgg": [
        (("this",), (("null_handling", "NULL ON NULL"),)),
        (("this",), (("null_handling", "ABSENT ON NULL"),)),
    ],
    # A COMMIT that says whether a new transaction starts where it ended.
    "Commit": [((), (("chain", True),)), ((), (("chain", False),))],
    "Cluster": [(("expressions[]",), ())],
    "Distribute": [(("expressions[]",), ())],
    "Sort": [(("expressions[]",), ())],
    # SYSTEM_VERSIONING turned off inside a WITH. The corpus writes the ON
    # form inside one and the OFF form outside, so the fourth combination was
    # never observed -- and the port reads all four.
    "WithSystemVersioningProperty": [((), (("on", False), ("with_", True)))],
    # An IF statement with an ELSE. The corpus writes only the form without
    # one, so no shape was observed for it -- and the port reads both.
    "IfBlock": [(("this", "true", "false"), ())],
    "Ceil": [(("this", "to"), ())],
    "Floor": [(("this", "to"), ())],
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
    # Which slots carry a coercion that is IDEMPOTENT: the dialect casts them
    # whatever it is given, so an argument already carrying that cast leaves
    # nothing to add and the plain spelling is exact.
    idem: dict = {}
    shapes = observed_shapes(exp, dialect, repo)
    for cls_name, extra in EXTRA_SHAPES.items():
        shapes.setdefault(cls_name, set()).update(extra)
    # Every bare property the parser can now READ needs a spelling to be
    # written back with. Taking them from the same probe keeps the two halves
    # together: a word the parser learns cannot become a node nobody can write.
    for cls_name in set(create_words(dialect)[0].values()):
        shapes.setdefault(cls_name, set()).add(((), ()))
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
            # The same shape written over LITERALS. Where a dialect spells a
            # node as an OPERATOR, it brackets an operand by precedence: DuckDB
            # writes `d + INTERVAL 1 DAY` for a literal and
            # `d + INTERVAL (x) DAY` for anything else. One probe alone
            # recorded whichever form it happened to see and wrote that for
            # both, so the two are compared and the difference recorded as a
            # property of the KEY rather than baked into the template.
            plain = {}
            for k in expr_keys:
                bare = k[:-2] if k.endswith("[]") else k
                lit = exp.Literal.number(1)
                plain[bare] = lit
                kwargs[bare] = [lit] if k.endswith("[]") else lit
            try:
                literal_text = _render(exp, cls(**kwargs), dialect)
            except Exception:  # noqa: BLE001
                literal_text = None
            ok = True
            bracketed = []
            for key in [k[:-2] if k.endswith("[]") else k for k in expr_keys]:
                token = f"ZZ{key.upper()}ZZ"
                if text.count(token) != 1:
                    ok = False
                    break
                # A token the column form wraps in parentheses and the literal
                # form does not is one the dialect brackets by PRECEDENCE. The
                # template keeps it bare and the writer puts the brackets back
                # where the operand needs them.
                at = text.index(token)
                wrapped = (
                    at > 0
                    and text[at - 1] == "("
                    and text[at + len(token) : at + len(token) + 1] == ")"
                )
                if wrapped and literal_text is not None and "(1)" not in literal_text:
                    bracketed.append(key)
                    text = text[: at - 1] + token + text[at + len(token) + 1 :]
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
            # An INFIX template -- marker, operator, marker -- is kept now that
            # the operands it brackets are recorded beside it. `a #> b` needs
            # parentheses around a child by PRECEDENCE, and the writer puts
            # them there: the leading operand is guarded by the same rule that
            # guards every template beginning with a marker, and the rest by
            # the Bracketed keys above.
            # A CAST the probe did not ask for is a COERCION the reference
            # applies by type: DuckDB writes BOOL_OR(CAST(x AS BOOLEAN)) only
            # when x is not already boolean. The probe feeds plain columns, so
            # any cast here was added, and baking it into the template wrapped
            # an argument that already had one.
            if "CAST(" in text and cls_name not in ("Cast", "TryCast"):
                # The coercion is IDEMPOTENT, though: an operand that already
                # carries that cast is written as it stands, because there is
                # nothing left to add. So the shape is probed again with the
                # cast already in place, and the template that comes back --
                # `BOOL_OR({this})` -- is exact for exactly those operands.
                # Which they are is the cast-sensitivity table's answer, and
                # the writer asks it before applying this.
                recast = _recast_probe(exp, cls, kwargs, expr_keys, scalars, dialect)
                if recast is None:
                    continue
                text, bracketed, idempotent = recast
                for _k, _ts in idempotent.items():
                    idem.setdefault(cls_name, {}).setdefault(_k, set()).update(_ts)
            # An INTERVAL the probe did not ask for is a unit the reference
            # SUPPLIED, the same way a cast above is a coercion it supplied:
            # DuckDB writes `d + INTERVAL (x) DAY` for an operand that names
            # no unit and `d + INTERVAL '1' DAY` for one that does, inventing
            # the DAY in the first. Baking that in wrote the invented unit
            # beside an operand that had brought its own.
            if "INTERVAL" in text and not any(
                k == "unit" or k == "unit[]" for k in expr_keys
            ) and not any(k == "unit" for k, _ in scalars):
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
            out.setdefault(cls_name, []).append((keys, marked, required, text, bracketed))
    return out, idem


def cast_target(how):
    """The type a nested Cast in a spec casts to, if it is one."""
    if how.get("nested") != "Cast":
        return None
    for _, inner in how["spec"]:
        if inner.get("node") == "DataType":
            for k, v in inner.get("extras") or ():
                if k == "this" and isinstance(v, Enum):
                    return v.value
    return None


def cast_elisions(exp, dialect, forms):
    """Casts the reference's GENERATOR does not write.

    The parser puts them there -- T-SQL's LEN(x) is a Length over a cast to
    TEXT -- and then the generator takes them straight back out, so `LEN(x)`
    is what goes in and what comes out. A port that built the tree faithfully
    and wrote it faithfully still differed, because faithful in one direction
    is not faithful in both.

    Two shapes, and both are asked rather than assumed: a cast at a KEY of
    some class that the class writes through, and a cast that writes as its
    own operand whatever holds it. What is probed is only the classes whose
    own builders make casts, since those are the only ones that can hold a
    cast nobody wrote.
    """
    by_key: dict = {}
    over: dict = {}
    seen = set()
    for _, cls_name, spec in forms:
        cls = getattr(exp, cls_name, None)
        if cls is None:
            continue
        others = [
            (k, exp.column(f"__probe_{i}"))
            for i, (k, how) in enumerate(spec)
            if "index" in how or "varlen" in how
        ]
        for key, how in spec:
            target = cast_target(how)
            if target is None or (cls_name, key, target) in seen:
                continue
            seen.add((cls_name, key, target))
            bare = exp.column("__held")
            try:
                cast = exp.cast(bare.copy(), target)
            except Exception:  # noqa: BLE001 -- not a type this dialect knows
                continue
            kwargs = {k: v for k, v in others if k != key}
            try:
                with_cast = cls(**{**kwargs, key: cast}).sql(dialect=dialect or None)
                without = cls(**{**kwargs, key: bare.copy()}).sql(dialect=dialect or None)
            except Exception:  # noqa: BLE001 -- a node that will not render
                continue
            if with_cast == without:
                by_key.setdefault(cls_name, {}).setdefault(key, set()).add(target)
    return by_key, over


def cast_over_elisions(exp, dialect, forms):
    """Casts that write as their OPERAND, whatever holds them.

    PostgreSQL's DIV builds a Cast to DECIMAL over an IntDiv and then writes
    just `DIV(4, 2)` -- the cast never appears, at the top of a statement or
    anywhere inside one. So it is a property of the cast alone, not of what
    holds it, and it is recorded by the type it casts to and the class it
    casts.
    """
    out: dict = {}
    seen = set()
    for _, cls_name, spec in forms:
        if cls_name != "Cast":
            continue
        target = None
        held = None
        for key, how in spec:
            if key == "to" and how.get("node") == "DataType":
                for k, v in how.get("extras") or ():
                    if k == "this" and isinstance(v, Enum):
                        target = v.value
            if key == "this":
                held = how.get("nested")
        if target is None or held is None or (target, held) in seen:
            continue
        seen.add((target, held))
        try:
            node = getattr(exp, held)(
                this=exp.column("__a"), expression=exp.column("__b")
            )
            whole = exp.cast(node.copy(), target).sql(dialect=dialect or None)
            alone = node.sql(dialect=dialect or None)
        except Exception:  # noqa: BLE001 -- a shape this dialect will not render
            continue
        if whole == alone:
            out.setdefault(target, set()).add(held)
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
        # A spec with a nested node describes a call whose arguments are not
        # the node's own -- T-SQL's LEN(x) builds Length(Cast(x, TEXT)). For
        # PARSING that matters; for WRITING it does not, because the writer
        # puts whatever the key holds where the key goes. So the wrapper is
        # dropped here and the node probed by its OWN keys, which is how
        # Databricks' AtTimeZone got its FROM_UTC_TIMESTAMP spelling back:
        # skipping these left the port writing the `AT TIME ZONE` operator
        # for a node the reference writes as a call.
        spec = [
            (key, {"index": i} if "nested" in how else how)
            for i, (key, how) in enumerate(spec)
        ] if any("nested" in how for _, how in spec) else spec
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
        # A call whose only positional argument is a LIST takes as many as it
        # is given, and each count may be spelled differently: T-SQL writes
        # `CONCAT(a, b)` for two and just `a` for one. One-element probes
        # recorded only the empty form, so the list is widened here.
        if len(positional) == 1 and "varlen" in positional[0][1]:
            key = positional[0][0]
            # Widest first, and the NARROWEST count that still writes the
            # plain call is what the spelling is recorded for: T-SQL drops the
            # call at one argument, so its spelling applies from two.
            lowest = 0
            for count in range(4, 0, -1):
                members = [exp.column(f"__probe_{i}") for i in range(count)]
                narrowed = dict(consts)
                narrowed.update({k: n for k, n, _ in nodes})
                narrowed[key] = members
                try:
                    rendered = cls(**narrowed).sql(dialect=dialect or None)
                except Exception:  # noqa: BLE001
                    continue
                expected = name + "(" + ", ".join(m.sql() for m in members) + ")"
                if rendered == expected:
                    lowest = count
            if lowest:
                entry = (
                    name,
                    [key],
                    consts + [(k, text) for k, _, text in nodes],
                    False,
                    lowest,
                )
                if entry not in candidates:
                    candidates.append(entry)
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
        known = {k for cand in candidates for k in list(cand[1]) + [c[0] for c in cand[2]]}
        for i, cand in enumerate(candidates):
            keyword, keys, consts, no_parens = cand[:4]
            min_args = cand[4] if len(cand) > 4 else 0
            named = set(keys) | {c[0] for c in consts}
            extra = [(k, None) for k in sorted(known - named)]
            candidates[i] = (keyword, keys, consts + extra, no_parens, min_args)
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
        candidates.sort(key=lambda c: (-len(c[2]), -(c[4] if len(c) > 4 else 0)))
    return out


def sqlmap(name: str, rendered) -> str:
    lines = []
    for cls in sorted(rendered):
        entries = []
        for entry in rendered[cls]:
            keyword, keys, consts, no_parens = entry[:4]
            min_args = entry[4] if len(entry) > 4 else 0
            arg_keys = ", ".join(gostr(k) for k in keys)
            const_parts = ", ".join(f"{{{gostr(k)}, {goconst(v)}}}" for k, v in consts)
            entries.append(
                f"{{{gostr(keyword)}, []string{{{arg_keys}}}, "
                f"[]FuncConst{{{const_parts}}}, {str(no_parens).lower()}, {min_args}}}"
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


# The Go type each of the reference's enums is spelled with in the port.
GO_ENUM_TYPES = {"DType": "DataTypeKind"}


def goconst(v) -> str:
    if v is None:
        return "nil"
    if isinstance(v, Enum):
        # The port names the same enum with a Go type of its own: a cast's
        # target is a DataTypeKind, not a bare string, and a bare string in
        # the slot would compare unequal to every kind the port builds.
        return "%s(%s)" % (GO_ENUM_TYPES.get(type(v).__name__, "string"), gostr(v.value))
    if isinstance(v, bool):
        return str(v).lower()
    if isinstance(v, int):
        return str(v)
    return gostr(v)


def funcargs(spec) -> str:
    parts = []
    for key, how in spec:
        if "nested" in how:
            # A node built AROUND the arguments rather than from one of them:
            # the class here, its own arguments described the same way one
            # level down.
            inner = funcargs(how["spec"])
            escapes = "".join(
                "%s, " % gostr(k) for k in sorted(how.get("except") or ())
            )
            annot_go = goannot(how.get("annot"))
            parts.append(
                '{%s, -1, false, nil, "", nil, %s, []FuncArg{%s}, []string{%s}, %s}'
                % (gostr(key), gostr(how["nested"]), inner, escapes, annot_go)
            )
        elif "node" in how:
            # Index -1 marks a CONSTANT node rather than a wrapper: the class
            # goes in the same slot, and the index tells the two apart.
            extras = "".join(
                "{%s, %s}, " % (gostr(k), goconst(v)) for k, v in (how.get("extras") or [])
            )
            parts.append(
                '{%s, -1, false, nil, %s, []FuncConst{%s}, "", nil, nil, nil}'
                % (gostr(key), gostr(how["node"]), extras)
            )
        elif "wrap" in how:
            extras = "".join(
                "{%s, %s}, " % (gostr(k), goconst(v)) for k, v in (how.get("extras") or [])
            )
            parts.append(
                '{%s, %d, false, nil, %s, []FuncConst{%s}, "", nil, nil, nil}'
                % (gostr(key), how["index"], gostr(how["wrap"]), extras)
            )
        elif "index" in how:
            parts.append(
                '{%s, %d, false, nil, "", nil, "", nil, nil, %s}'
                % (gostr(key), how["index"], goannot(how.get("annot")))
            )
        elif "varlen" in how:
            parts.append('{%s, %d, true, nil, "", nil, "", nil, nil, nil}' % (gostr(key), how["varlen"]))
        else:
            parts.append(
                '{%s, -1, false, %s, "", nil, "", nil, nil, nil}' % (gostr(key), goconst(how["const"]))
            )
    return ", ".join(parts)


def funcmap(name: str, funcs, annots=None) -> str:
    """One spec per name, sharing funcargs so the two tables cannot drift."""
    lines = []
    annots = annots or {}
    for fn in sorted(funcs):
        cls, spec = funcs[fn]
        lines.append(
            f"\t\t\t{gostr(fn)}: {{{gostr(cls)}, []FuncArg{{{funcargs(spec)}}}, "
            f"{goannot(annots.get(fn))}}},\n"
        )
    return f"\t\t{name}: map[string]FuncSpec{{\n{''.join(lines)}\t\t}},\n"


def bare_name_is_column(dialect: str) -> set[str]:
    """No-paren function names that a BARE occurrence reads as a column.

    Some of these parsers retreat when nothing usable follows -- `SELECT next`
    is a column called next, because NEXT wanted `VALUE FOR` and did not find
    it. Others do not: Databricks reads a bare CURDATE as CURRENT_DATE, taking
    no argument at all. Nothing in the parser says which is which, so each name
    is asked by parsing one.
    """
    import sqlglot
    from sqlglot import expressions as e
    from sqlglot.dialects.dialect import Dialect

    parser = Dialect.get_or_raise(dialect or None).parser_class
    out: set[str] = set()
    for name in parser.NO_PAREN_FUNCTION_PARSERS:
        try:
            tree = sqlglot.parse_one(f"SELECT {name}", dialect=dialect or None)
        except Exception:
            continue
        projections = tree.args.get("expressions") or []
        if len(projections) != 1:
            continue
        only = projections[0]
        if isinstance(only, e.Column) and only.name.upper() == name.upper():
            out.add(name)
    return out


def colon_lambda(dialect: str) -> tuple[bool, str]:
    """Whether this dialect READS `LAMBDA a, b : body`, and how it WRITES one.

    DuckDB spells a lambda twice over -- `x -> x + 1` and `LAMBDA x : x + 1` --
    and records which was written on the node itself. Every other dialect drops
    the distinction and writes the arrow, so the template comes back empty and
    the port's ordinary lambda writer keeps the node.

    The template is read off a rendered node rather than transcribed: two are
    rendered, with one parameter and with two, and the second has to agree with
    the template the first produced or nothing is recorded.
    """
    import sqlglot
    from sqlglot import expressions as e

    reads = False
    try:
        tree = sqlglot.parse_one(
            "SELECT LIST_TRANSFORM(c, LAMBDA aaa : aaa)", dialect=dialect or None
        )
        only = (tree.args.get("expressions") or [None])[0]
        inner = only.args.get("expression") if only is not None else None
        reads = isinstance(inner, e.Lambda) and bool(inner.args.get("colon"))
    except Exception:
        reads = False

    def render(count: int, colon: bool) -> str:
        names = [e.to_identifier("aaa"), e.to_identifier("ccc")][:count]
        node = e.Lambda(this=e.column("bbb"), expressions=names, colon=colon or None)
        return node.sql(dialect=dialect or None)

    one, two = render(1, True), render(2, True)
    if one == render(1, False) and two == render(2, False):
        return reads, ""
    template = one.replace("aaa", "{expressions}").replace("bbb", "{this}")
    if template.replace("{expressions}", "aaa, ccc").replace("{this}", "bbb") != two:
        return reads, ""
    return reads, template


def struct_spelling(dialect: str) -> tuple[str, str]:
    """How this dialect writes a struct and one of its named fields.

    Two spellings that are not variations of each other: `STRUCT(v AS k)` puts
    the value first and names it after, DuckDB's `{'k': v}` puts the key first
    and quotes it as a string. Both templates are read off rendered nodes --
    an empty struct against a one-field one for the wrapper, then a key that
    needs quoting to settle whether the key is written as an identifier or as
    its bare name inside quotes.
    """
    from sqlglot import expressions as e

    def struct(keys: list[str]) -> str:
        fields = [
            e.PropertyEQ(this=e.to_identifier(k), expression=e.column(f"v{i}"))
            for i, k in enumerate(keys)
        ]
        return e.Struct(expressions=fields).sql(dialect=dialect or None)

    empty, one = struct([]), struct(["kkk"])
    prefix = ""
    for i, ch in enumerate(empty):
        if i < len(one) and one[i] == ch:
            prefix += ch
        else:
            break
    suffix = empty[len(prefix) :]
    if not one.startswith(prefix) or not one.endswith(suffix):
        return "", ""
    wrapper = f"{prefix}{{fields}}{suffix}"

    field = one[len(prefix) : len(one) - len(suffix)]
    field = field.replace("v0", "{value}")
    odd = struct(["k-k"])
    odd_field = odd[len(prefix) : len(odd) - len(suffix)]
    quoted = e.to_identifier("k-k").sql(dialect=dialect or None)
    for name, marker in (("kkk", "{name}"), ("kkk", "{key}")):
        candidate = field.replace(name, marker)
        want = "k-k" if marker == "{name}" else quoted
        if candidate.replace(marker, want).replace("{value}", "v0") == odd_field:
            return wrapper, candidate
    return "", ""


def property_specs(dialect: str) -> dict[str, dict]:
    """What each table property is called and what it takes after its name.

    The reference keeps 92 of these in a table of closures, one little grammar
    each, and none of them is readable as data. So each name is asked: six
    candidate spellings are parsed and the answer is classified by the SHAPE of
    the node that came back, not by the name.

      bare             no argument at all -- EXTERNAL, HEAP, ICEBERG
      value            an optional `=` and then a string or a word, into `this`
      table            a table name, into `this` -- LIKE
      schema           a parenthesised column list, into `this` as a Schema
      wrapped-columns  a parenthesised list, into `expressions` as columns
      wrapped-tables   the same, as tables -- INHERITS

    A name whose answers fit none of them is not recorded, and the port keeps
    refusing it. One name is skipped for a different reason: DATA_DELETION with
    anything unexpected in its parentheses never returns -- see
    docs/upstream-issues.md -- so the probe bounds every parse with a timer
    rather than waiting on it.
    """
    import signal

    import sqlglot
    from sqlglot import expressions as e
    from sqlglot.dialects.dialect import Dialect

    class Slow(Exception):
        pass

    def raise_slow(*_):
        raise Slow()

    previous = signal.signal(signal.SIGALRM, raise_slow)

    def one(name: str, arg: str):
        signal.setitimer(signal.ITIMER_REAL, 2.0)
        try:
            tree = sqlglot.parse_one(f"CREATE TABLE t (a INT) {name}{arg}", read=dialect or None)
        except Exception:  # noqa: BLE001 -- including the timer
            return None
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
        found = tree.args.get("properties") if isinstance(tree, e.Create) else None
        items = found.args["expressions"] if found is not None else []
        return items[0] if len(items) == 1 else None

    out: dict[str, dict] = {}
    try:
        for name in sorted(Dialect.get_or_raise(dialect or None).parser_class.PROPERTY_PARSERS):
            bare = one(name, "")
            wrapped = one(name, " (aaa)")
            word = one(name, " xxx")
            shape = None
            node = None
            if wrapped is not None:
                inner = wrapped.args.get("this")
                listed = wrapped.args.get("expressions") or []
                if isinstance(inner, e.Schema):
                    shape, node = "schema", wrapped
                elif listed and all(isinstance(x, e.Column) for x in listed):
                    shape, node = "wrapped-columns", wrapped
                elif listed and all(isinstance(x, e.Table) for x in listed):
                    shape, node = "wrapped-tables", wrapped
            if shape is None and word is not None:
                inner = word.args.get("this")
                if isinstance(inner, e.Var):
                    shape, node = "value", word
                elif isinstance(inner, e.Table):
                    shape, node = "table", word
            if shape is None and bare is not None and not bare.args:
                shape, node = "bare", bare
            if shape is None:
                # A word that opens a LIST of other properties -- `WITH (...)`,
                # `TBLPROPERTIES (...)`. Recognised by putting a property the
                # probe already knows inside it and seeing THAT one come back.
                # Asked only of a word with no shape of its own, or COMMENT
                # would answer yes to its own class.
                nested = one(name, " (COMMENT='sss')")
                if nested is not None and type(nested).__name__ == "SchemaCommentProperty":
                    out[name] = {
                        "class": "",
                        "shape": "wrapped-properties",
                        "eq": False,
                        "named": False,
                    }
                continue
            if node is None:
                continue
            # Whether the word takes a NAME before its list: T-SQL's `ON b (c)`
            # names a partition scheme and the column it splits by, and the
            # reference builds a Schema over both. A word that does not is
            # handed the whole of `b (c)` as one call -- which is what the
            # port wrote it back as, upper-cased and without its space.
            named = False
            if shape == "schema":
                both = one(name, " zzb (zzc)")
                if both is not None:
                    inner = both.args.get("this")
                    named = isinstance(inner, e.Schema) and inner.args.get("this") is not None
            out[name] = {
                "class": type(node).__name__,
                "shape": shape,
                "named": named,
                # Whether an equals sign may stand between the word and what
                # follows it. Asked of the shape's OWN spelling: a value takes
                # `FORMAT='parquet'` and a schema takes
                # `PARTITIONED_BY=(x INT)`.
                "eq": (
                    one(name, "='sss'") is not None
                    if shape == "value"
                    else shape == "schema" and one(name, "=(aaa INT)") is not None
                ),
            }
    finally:
        signal.signal(signal.SIGALRM, previous)
    return out


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
        "\t// SizedTypeSQL is the name a type takes when it carries PARAMETERS,\n",
        "\t// where that differs: Databricks writes a bare VARCHAR as STRING and\n",
        "\t// a VARCHAR(255) as itself.\n",
        "\tSizedTypeSQL map[string]string\n",
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
        "\t// InvalidFuncNameTokens are the token types that never name a\n",
        "\t// call, however the name reads: a QUOTED identifier or a string\n",
        "\t// is a name and nothing else, so a quoted CASE is a column even\n",
        "\t// though the bare word opens one.\n",
        "\tInvalidFuncNameTokens map[TokenType]struct{}\n",
        "\t// ValuesFollowedByParen says a bare VALUES -- one with no argument\n",
        "\t// list after it -- is a column name in this dialect rather than\n",
        "\t// the start of a VALUES clause.\n",
        "\tValuesFollowedByParen bool\n",
        "\t// BareNameIsColumn holds the no-paren function names that read as\n",
        "\t// a COLUMN when nothing usable follows them. Probed one name at a\n",
        "\t// time: NEXT retreats without its `VALUE FOR` and CURDATE does not\n",
        "\t// retreat at all.\n",
        "\tBareNameIsColumn map[string]struct{}\n",
        "\t// ColonLambdaRead says the dialect reads `LAMBDA a, b : body`, and\n",
        "\t// ColonLambdaWrite is the template it writes one back with -- empty\n",
        "\t// where the dialect has no spelling of its own and the ordinary\n",
        "\t// arrow form is used instead.\n",
        "\tColonLambdaRead  bool\n",
        "\tColonLambdaWrite string\n",
        "\t// FunctionsWithAliasedArgs are the calls whose arguments may name\n",
        "\t// themselves even though the call itself is one the port builds a\n",
        "\t// node for: `STRUCT(1 AS a)` is a Struct of one named field.\n",
        "\tFunctionsWithAliasedArgs map[string]struct{}\n",
        "\t// StructTemplate wraps a struct's fields and StructFieldTemplate\n",
        "\t// writes one named field. `{key}` is the field name written as an\n",
        "\t// identifier and `{name}` is the bare name, which the dialect that\n",
        "\t// quotes its keys as strings uses instead.\n",
        "\tStructTemplate      string\n",
        "\tStructFieldTemplate string\n",
        "\t// PropertySpecs are the words a CREATE may say about the thing it\n",
        "\t// makes, keyed by the word. Class is the node built and Shape is\n",
        "\t// what follows the word; both are probed by parsing one, because\n",
        "\t// the reference keeps a little grammar per name and none of it is\n",
        "\t// readable as data.\n",
        "\tPropertySpecs map[string]PropertySpec\n",
        "\t// PropertyLocation says WHERE each property class is written --\n",
        "\t// POST_SCHEMA ones stand on their own after the columns, POST_WITH\n",
        "\t// ones go together inside one wrapped list.\n",
        "\tPropertyLocation map[string]string\n",
        "\t// WithPropertiesPrefix opens that wrapped list: WITH everywhere but\n",
        "\t// Databricks, which writes TBLPROPERTIES.\n",
        "\tWithPropertiesPrefix string\n",
        "\t// PropertyNameQuoted says a plain key-and-value property writes its\n",
        "\t// KEY in quotes -- Databricks writes `'a.b'=15` for what the others\n",
        "\t// write `a.b=15`. Read off a rendered node.\n",
        "\tPropertyNameQuoted bool\n",
        "\t// StringArgWraps are the positions where a builder REWRITES a\n",
        "\t// string argument rather than carrying it: T-SQL's DATETRUNC casts\n",
        "\t// one to DATETIME2, PostgreSQL's GENERATE_SERIES turns a step of\n",
        "\t// `'1 day'` into an INTERVAL. The port does the rewrite itself\n",
        "\t// before building, so a string that only moves in this way is not\n",
        "\t// evidence of a builder nobody can describe.\n",
        "\tStringArgWraps map[string]map[int]string\n",
        "\t// CallNamesTheReferenceKnows are every name the reference reads as\n",
        "\t// something other than an anonymous call -- a builder, a parser of\n",
        "\t// its own, a no-paren form, or a quantifier over a subquery. A name\n",
        "\t// NOT here is one the reference reads anonymously too, which is what\n",
        "\t// lets the annotator answer UNKNOWN for it without hiding a parse\n",
        "\t// gap behind the answer.\n",
        "\tCallNamesTheReferenceKnows map[string]struct{}\n",
        "\t// BareProcedureWrapper is the class a CREATE PROCEDURE's name is\n",
        "\t// wrapped in when it was written WITHOUT a parameter list. T-SQL\n",
        "\t// puts a StoredProcedure round it and the rest leave the name\n",
        "\t// alone, so the empty string means no wrapper.\n",
        "\tBareProcedureWrapper string\n",
        "\t// ProcedureWithOptions are the words a CREATE PROCEDURE may say\n",
        "\t// after WITH, and the class each becomes. The reference reads two\n",
        "\t// of them as a view attribute and the rest as a procedure option,\n",
        "\t// which is a distinction nothing in the words themselves makes --\n",
        "\t// so each is asked by parsing one.\n",
        "\tProcedureWithOptions map[string]string\n",
        "\t// ColumnDefaultAfterEquals says a column definition may be followed\n",
        "\t// by `= <value>`, which becomes its default. T-SQL alone reads it,\n",
        "\t// and it is how a procedure parameter is given one.\n",
        "\tColumnDefaultAfterEquals bool\n",
        "\t// IfIsAStatement says a bare IF opens a STATEMENT in this dialect --\n",
        "\t// `IF <cond> BEGIN ... END` -- rather than being a call or a name.\n",
        "\tIfIsAStatement bool\n",
        "\t// AlterCollateIsVar says the name a COLLATE gives is kept as a Var\n",
        "\t// rather than as an Identifier, and AlterColumnTypeTakesNull that a\n",
        "\t// retyped column may say NOT NULL or NULL after its new type.\n",
        "\tAlterCollateIsVar        bool\n",
        "\tAlterColumnTypeTakesNull bool\n",
        "\t// AlterColumnNullabilityWritten says a retyped column's NOT NULL is\n",
        "\t// written back. Where it is not, the reference drops the words and\n",
        "\t// this port refuses rather than writing a column that takes nulls\n",
        "\t// when the statement said it must not.\n",
        "\tAlterColumnNullabilityWritten bool\n",
        "\t// OpclassFollowWords and OpclassFollowTokens are what may follow an\n",
        "\t// indexed column WITHOUT being an operator class for it: a word\n",
        "\t// that orders the column, or the punctuation that ends it.\n",
        "\tOpclassFollowWords  map[string]struct{}\n",
        "\tOpclassFollowTokens map[TokenType]struct{}\n",
        "\t// TrimTypes are the words that may say WHERE a TRIM trims, and\n",
        "\t// TrimPatternFirst that its comma form writes the characters to\n",
        "\t// trim before the string they are trimmed from.\n",
        "\tTrimTypes        map[string]struct{}\n",
        "\tTrimPatternFirst bool\n",
        "\t// ProjectionEqualsIsAlias reads `SELECT a = 1` as `1 AS a` rather\n\t// than as a comparison. Only T-SQL does, and it is the same text\n\t// meaning two different things, so nothing but the dialect settles it.\n\tProjectionEqualsIsAlias bool\n",
        "\t// PositionalColumns reads `#2` as the SECOND output column rather\n\t// than as anything to do with a hash.\n\tPositionalColumns bool\n",
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
        "\t// InverseTimeMapping spells a stored format back out again, and\n",
        "\t// FormatTimeMapping is the SEPARATE table a one-character format\n",
        "\t// is read through. Only the dialects whose FORMAT is a builder\n",
        "\t// that reads its argument have the second one, which is what\n",
        "\t// tells the port that builder is installed.\n",
        "\t// DatePartMapping normalises a date part's spelling: T-SQL records\n",
        "\t// DATEPART(mm, x) as MONTH. A part the map does not name is kept\n",
        "\t// exactly as it was written, case and all.\n",
        "\tDatePartMapping map[string]string\n",
        "\tInverseTimeMapping map[string]string\n",
        "\tFormatTimeMapping  map[string]string\n",
        "\tCastSensitiveArgs map[string]map[int][]string\n",
        "\t// CastCoercions is what the dialect wraps an argument in, per the\n\t// type it is cast to: DuckDB rounds a float into a BIT_OR and casts a\n\t// decimal without rounding. A wrapper of `{arg}` alone means the slot\n\t// takes that type as it stands.\n\tCastCoercions map[string]map[int]map[string]map[string]string\n",
        "\t// CastIdempotentTypes are the slots whose coercion the dialect\n\t// applies whatever it is given, so an argument already carrying that\n\t// cast leaves nothing to add and the plain spelling is exact.\n\tCastIdempotentTypes map[string]map[string][]string\n",
        "\t// CastElidedAt names, per class and per key, the cast TARGETS the\n\t// reference's generator does not write there. Its own parser puts them\n\t// in -- T-SQL's LEN(x) is a Length over a cast to TEXT -- and its\n\t// generator takes them straight back out, so a port faithful in one\n\t// direction and not the other wrote LEN(CAST(x AS VARCHAR(MAX))).\n\tCastElidedAt map[string]map[string][]string\n",
        "\t// CastElidedOver names, per cast TARGET, the classes a cast to that\n\t// type is never written around: PostgreSQL's DIV is a Cast to DECIMAL\n\t// over an IntDiv, written as just DIV(4, 2).\n\tCastElidedOver map[string][]string\n",
        "\t// CastSensitiveTypes says WHICH cast targets move the rendering in\n\t// each of those positions. A slot is not sensitive to casting as\n\t// such: DuckDB wraps a non-text argument to UPPER in a cast to TEXT,\n\t// and leaves one that is already TEXT alone.\n\tCastSensitiveTypes map[string]map[int]map[string][]string\n",
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
        "\t// NormalizeNotNull: `x NOTNULL` is written NOT x IS NULL here and\n",
        "\t// kept as a negated IS elsewhere -- two trees for one word.\n",
        "\tNormalizeNotNull bool\n",
        "\t// StarExceptWord is what `* EXCEPT (a)` is written with: DuckDB\n",
        "\t// says EXCLUDE, and both words are READ everywhere.\n",
        "\tStarExceptWord string\n",
        "\t// DeclareAssignment is what stands between a declared variable and\n",
        "\t// its initial value.\n",
        "\tDeclareAssignment string\n",
        "\t// EndCommits: a bare END ENDS THE TRANSACTION here, and is a name\n",
        "\t// or a block anywhere else.\n",
        "\tEndCommits bool\n",
        "\t// FormatSpellings says how each class that carries a FORMAT writes\n",
        "\t// it: `inverse` through the dialect's own mapping -- the tree\n",
        "\t// stores %Y-%m-%d and PostgreSQL writes YYYY-MM-DD -- `verbatim`\n",
        "\t// as stored, and `other` through a table of its own, which the\n",
        "\t// writer declines rather than guess at.\n",
        "\tFormatSpellings map[string]string\n",
        "\t// DefaultTimeFormat is the format a `default-dropped` class writes\n",
        "\t// as nothing at all.\n",
        "\tDefaultTimeFormat string\n",
        "\t// LikeInsideSchema: a `LIKE other` that is NOT inside a column\n",
        "\t// list brings its own parentheses here. SupportsCreateLike says\n",
        "\t// the dialect writes the word at all.\n",
        "\tLikeInsideSchema   bool\n",
        "\tSupportsCreateLike bool\n",
        "\t// ExecuteBuildsExecute: EXEC names a procedure to run here, and is\n",
        "\t// kept as raw text anywhere else.\n",
        "\tExecuteBuildsExecute bool\n",
        "\t// ShowKinds are the phrases SHOW reads as a statement rather than\n",
        "\t// keeping as text.\n",
        "\tShowKinds map[string]struct{}\n",
        "\t// ReservedTokens are the ones that can never stand in for a name,\n",
        "\t// which is what the reference asks where ANY token may.\n",
        "\tReservedTokens map[TokenType]struct{}\n",
        "\t// SequenceOptions are the words a CREATE SEQUENCE takes that carry\n",
        "\t// no value, each with the word that may follow it: NO CYCLE is one\n",
        "\t// option, not two.\n",
        "\tSequenceOptions map[string][]string\n",
        "\t// CreatableTokens are the things a CREATE makes and a DROP\n",
        "\t// removes, and CreatableKindNames what a dialect calls them where\n",
        "\t// it uses another word.\n",
        "\tCreatableTokens    map[TokenType]struct{}\n",
        "\tCreatableKindNames map[string]string\n",
        "\t// How a COPY's parameters are written: separated by commas or by\n",
        "\t// spaces, and with or without an = before each value. The names\n",
        "\t// whose value is a LIST are kept apart, since those are read a\n",
        "\t// different way again.\n",
        "\tCopyIntoWritten   bool\n",
        "\tCopyParamsWrapped bool\n",
        "\tCopyParamsAreCSV  bool\n",
        "\tCopyParamsNeedEQ  bool\n",
        "\tCopyVarlenOptions map[string]struct{}\n",
        "\t// UnaryOps is which token opens a PREFIX operator, and what it\n",
        "\t// builds. The empty string is the no-op unary plus.\n",
        "\tUnaryOps map[TokenType]string\n",
        "\t// PrefixCalls is punctuation that NAMES a function written in\n",
        "\t// front of its operand, keyed by the characters: DuckDB's `@x` is\n",
        "\t// ABS(x), and it takes a whole arithmetic expression.\n",
        "\tPrefixCalls map[string]string\n",
        "\t// TsOrDsParents are the classes a TsOrDsToDate DISAPPEARS under.\n",
        "\t// Databricks reads YEAR(y) as a Year over a TsOrDsToDate and writes\n",
        "\t// it back as YEAR(y) -- the wrapper is written only where its\n",
        "\t// parent is not one of these, which is a rule about the PARENT and\n",
        "\t// so invisible to a probe that renders a node on its own.\n",
        "\tTsOrDsParents map[string]struct{}\n",
        "\t// ConvertBuildsConvert: CONVERT(type, value) is a Convert here and a\n",
        "\t// CAST written another way everywhere else.\n",
        "\tConvertBuildsConvert bool\n",
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
        "\t// TruncatesCatalog: a three-part name is written with only two of\n",
        "\t// them here, which names a different object.\n",
        "\tTruncatesCatalog bool\n",
        "\t// HoistsInsertWith: a WITH inside an INSERT is written in FRONT of\n",
        "\t// the statement here, and left where it was written elsewhere.\n",
        "\tHoistsInsertWith bool\n",
        "\t// ReturningWord is what this dialect calls a RETURNING clause, and\n\t// ReturningEnd whether it is written after the WHERE rather than\n\t// straight after the verb.\n\tReturningWord string\n\tReturningEnd  bool\n",
        "\t// MergeWithoutTarget: a MERGE branch's assignments are written without\n\t// the target's own name in front of them here.\n\tMergeWithoutTarget bool\n\t// NormalizeUnquoted and NormalizeQuoted are the case a name is COMPARED\n\t// in -- lower, upper, or empty for as-written.\n\tNormalizeUnquoted string\n\tNormalizeQuoted   string\n",
        "	// CreateExistsWritten says, per kind, whether IF NOT EXISTS survives\n\t// being written. TemporaryWritten says the same of TEMPORARY.\n\tCreateExistsWritten map[string]bool\n\tTemporaryWritten    map[string]bool\n\t// ViewColumnCommentWritten: a view column keeps its COMMENT here.\n\tViewColumnCommentWritten bool\n",
        "\t// AlterAddColumnWord: an ALTER writes the word COLUMN after ADD here,\n\t// and AlterRepeatsAdd whether each added column gets its own ADD.\n\tAlterAddColumnWord bool\n\tAlterRepeatsAdd    bool\n\t// AlterColumnTypeWord is what comes between an altered column and its\n\t// new type -- SET DATA TYPE, TYPE, or nothing at all.\n\tAlterColumnTypeWord string\n",
        "\t// PrimaryKeyMembersOrdered: a table-level PRIMARY KEY names its columns\n\t// as ordered index members here, not as bare names.\n\tPrimaryKeyMembersOrdered bool\n\t// UniqueConstraintWritten: a UNIQUE constraint survives being written\n\t// here. Where it does not, the guarantee would be silently dropped.\n\tUniqueConstraintWritten bool\n",
        "\t// FunctionReturnsPlace says WHERE a RETURNS property is written, per\n\t// shape of what it holds: after the parameter list, in the body, or\n\t// nowhere at all.\n\tFunctionReturnsPlace map[string]string\n\t// FunctionPropertiesWritten: a function\'s other properties -- LANGUAGE,\n\t// IMMUTABLE, STRICT -- survive being written here.\n\tFunctionPropertiesWritten bool\n\t// FunctionReturnAs: AS is written in front of a RETURN body.\n\tFunctionReturnAs bool\n\t// FunctionWrapsTableBody: a table-valued function\'s body becomes a\n\t// RETURN here even when it was not written as one.\n\tFunctionWrapsTableBody bool\n\t// FunctionAsTableRead: `AS TABLE <query>` is READ as a return type here.\n\tFunctionAsTableRead bool\n\t// ReturnWord is the word a RETURN writes, empty where it writes none.\n\tReturnWord string\n",
        "\t// ParameterModePrefix: a parameter\'s mode is written in FRONT of it\n\t// here rather than after its type, and ParameterModeWords how each is\n\t// spelled -- one word or two.\n\tParameterModePrefix bool\n\tParameterModeWords  map[string]string\n",
        "\t// SetItemSeparator sits between a configuration name and its value.\n\tSetItemSeparator string\n",
        "\t// ComputedColumnSpelling is how a computed column is written, with\n\t// {expr} where the expression goes, and ComputedKeepsType whether the\n\t// column\'s declared type survives beside it.\n\tComputedColumnSpelling string\n\tComputedKeepsType      bool\n\t// IdentityWritten: a GENERATED ... AS IDENTITY column keeps that\n\t// spelling here rather than being rewritten into something else.\n\tIdentityWritten bool\n\t// IdentityWidensType: an identity column\'s type is widened to BIGINT.\n\tIdentityWidensType bool\n",
        "\t// GeneratedExpressionIsComputed: `GENERATED ALWAYS AS (x)` with no\n\t// STORED is a COMPUTED column here, not an identity with an\n\t// expression on it.\n\tGeneratedExpressionIsComputed bool\n",
        "\t// IndexOnWord sits between an index and the table it is on.\n\tIndexOnWord string\n",
        "\t// StringClassSQL is how each kind of quoted string is written, keyed by\n\t// class. The value is the template, a tab, and whether the body takes a\n\t// string\'s own escaping. A class missing from the map is one this\n\t// dialect writes in a way that loses the value.\n\tStringClassSQL map[string]string\n",
        "\t// OffsetRowsWord is written after an OFFSET count, and is empty in\n\t// every dialect but T-SQL.\n\tOffsetRowsWord string\n",
        "\t// IntervalUnitAliases are the unit spellings an INTERVAL normalises,\n\t// keyed by the upper-cased spelling written.\n\tIntervalUnitAliases map[string]string\n",
        "\t// TableHintsWritten: a table\'s locking hints survive here. Every\n\t// dialect but T-SQL drops them.\n\tTableHintsWritten bool\n",
        "\t// TransactionWord is written after BEGIN, COMMIT and ROLLBACK here.\n\t// TransactionSavepointWritten says whether the name a ROLLBACK TO\n\t// carries survives, and TransactionNameWritten whether the name the\n\t// TRANSACTION itself carries does. The dialects disagree about the two\n\t// in opposite directions.\n\tTransactionWord             string\n\tTransactionSavepointWritten bool\n\tTransactionNameWritten      bool\n",
        "\t// BareBeginIsATransaction: a BEGIN with no TRANSACTION after it opens\n\t// one here. In T-SQL it opens a block instead.\n\tBareBeginIsATransaction bool\n",
        "\t// SetItemKindWritten says whether a SET\'s scope word survives, per\n\t// word. Dropping one changes which scope the setting belongs to.\n\tSetItemKindWritten map[string]bool\n",
        "\t// SetItemVariableSeparator sits between a VARIABLE and its value,\n\t// which is not always what sits between a setting and its value.\n\tSetItemVariableSeparator string\n",
        "\t// SetWithoutASign: a SET may be written with no `=` between the name\n\t// and the value here.\n\tSetWithoutASign bool\n",
        "\t// PartitionSQL is how a table\'s PARTITION clause is written, with\n\t// {members} where its members go.\n\tPartitionSQL string\n",
        "\t// AlterSetOptionWritten: an `ALTER TABLE ... SET` keeps the words that\n\t// say WHAT it sets, and AlterSetWrapsOptions whether a list of\n\t// settings is written in parentheses.\n\tAlterSetOptionWritten bool\n\tAlterSetWrapsOptions  bool\n",
        "\t// AlterSetListIsSettings: `ALTER TABLE t SET (k = v)` is a list of\n\t// settings here rather than of table properties.\n\tAlterSetListIsSettings bool\n",
        "\t// AlterSetIsWrapped: `ALTER TABLE t SET (...)` puts the whole SET\n"
        "\t// inside parentheses of the syntax's own, and what is inside them\n"
        "\t// is read as PROPERTIES rather than as settings.\n"
        "\tAlterSetIsWrapped bool\n",
        "\t// KeyConstraintOptions are the words that may follow a REFERENCES or a\n\t// key, and what may follow each of them.\n\tKeyConstraintOptions map[string][]string\n",
        "\t// WithDataWritten: `WITH NO DATA` survives here. It says whether the\n\t// table is FILLED from the query or only shaped by it.\n\tWithDataWritten bool\n",
        "\t// EnumTypeSQL is how an ENUM type's members are written, with\n\t// {members} in it. PostgreSQL puts a space in front of the list and\n\t// writes the parentheses even when the list is empty; everyone else\n\t// spells it as an ordinary parameterised type.\n\tEnumTypeSQL string\n",
        "\t// IdentityShortSQL is the short spelling of an identity column, with\n\t// {start} and {increment} in it, and empty where the dialect writes\n\t// the whole GENERATED form instead.\n\tIdentityShortSQL string\n",
        "\t// AutoIncrementSQL is how an auto-incrementing column is spelled, and\n\t// is empty where the dialect writes nothing and drops the numbering.\n\tAutoIncrementSQL string\n",
        "\t// ValuesTableWrapped: a VALUES clause used as a TABLE is written in\n\t// parentheses here.\n\tValuesTableWrapped bool\n",
        "\t// RenameTarget says how much of a qualified name a RENAME TO writes:\n",
        "\t// the whole thing, the last part only, or -- empty -- neither,\n",
        "\t// because the dialect writes another statement entirely.\n",
        "\tRenameTarget string\n",
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
        "\t// WithinGroupInside: the ordering an ordered-set aggregate is\n\t// computed over is written INSIDE the call here rather than after it\n\t// in a WITHIN GROUP of its own. DuckDB is the only one, and it moves\n\t// a percentile's order key into the call's first argument as well.\n\tWithinGroupInside bool\n",
        "\t// PercentileClasses are the ordered-set aggregates whose ARGUMENTS a\n\t// dialect writing the order inside reshuffles: the key becomes the\n\t// first and the fraction slides right.\n\tPercentileClasses map[string]struct{}\n",
        "\t// IgnoreNullsInFunc: `IGNORE NULLS` is written INSIDE the call's\n\t// argument list here rather than after the call.\n\tIgnoreNullsInFunc bool\n",
        "\t// ConditionCoercion is how a value that is not already a condition\n\t// is made into one, with {value} standing for it. T-SQL has no boolean\n\t// type and compares against zero instead; empty where the dialect\n\t// takes a value as a condition unchanged.\n\tConditionCoercion string\n",
        "\t// SplitPartCountsBackwards is how a dialect that counts the pieces\n\t// of a DOTTED name from the other end opens that call, with {this} in\n\t// it; the number and the closing parenthesis are the writer's. Empty\n\t// where the dialect splits on any delimiter it is given.\n\tSplitPartCountsBackwards string\n",
        "\t// FileFormatSQL is how the storage format a table is written in is\n\t// spelled, with {name} standing for the format. Databricks says\n\t// `USING PARQUET` where the rest say `FORMAT=PARQUET`.\n\tFileFormatSQL string\n",
        "\t// TemporarySuffix is what a dialect APPENDS to a temporary object\n\t// of each kind: Databricks gives a temporary TABLE a storage format\n\t// it was never given.\n\tTemporarySuffix map[string]string\n",
        "\t// DateDeltaIsAnOperator: a date shifted by an interval is written\n\t// with an OPERATOR here -- `d + INTERVAL 1 DAY` -- rather than as a\n\t// call. The unit rides on the interval either way.\n\tDateDeltaIsAnOperator bool\n",
        "\t// ColumnCommentWritten: a COMMENT on a column survives here.\n\t// PostgreSQL and DuckDB have nowhere to say it in a CREATE and write\n\t// nothing, which is what the reference does with it.\n\tColumnCommentWritten bool\n",
        "\t// CommaUnnestJoins: a comma join over an UNNEST is written here as\n\t// an explicit JOIN with `ON TRUE`, because the comma form does not\n\t// bind the unnested rows to the row they came from.\n\tCommaUnnestJoins bool\n",
        "\t// TableSample is how a sampling clause is written: the words that\n\t// open it, whether the METHOD is written, whether a bare size counts\n\t// ROWS or a percentage, and what the repeatable seed is called.\n\tTableSample TableSampleSQL\n",
        "\t// JSONExtractTwiceSQL is how a dialect that writes the value TWICE\n\t// spells an extraction: T-SQL asks JSON_QUERY for an object and\n\t// JSON_VALUE for a scalar and takes whichever is not null. Empty where\n\t// the dialect writes the value once.\n\tJSONExtractTwiceSQL map[string]string\n",
        "\t// RegexpFlagArgs names which argument of each regexp call holds the\n\t// FLAGS: `modifiers` on a replacement, `parameters` on an extraction.\n\tRegexpFlagArgs map[string]string\n",
        "\t// RegexpFlags are the flag characters a REGEXP_REPLACE may carry\n\t// here, and RegexpFlagsNeedLiteral whether they have to be written as\n\t// a string. An empty RegexpFlags means the dialect writes whatever it\n\t// is given; a dialect that writes none at all has\n\t// RegexpFlagsWritten false and the port refuses rather than dropping\n\t// them, because a flag says what the replacement DOES.\n\tRegexpFlags            string\n\tRegexpFlagsWritten     bool\n\tRegexpFlagsNeedLiteral bool\n",
        "\t// IgnoreNullsWindowFuncs are the calls that KEEP their null\n\t// treatment where the dialect writes it inside; IgnoreNullsDropped\n\t// are the ones it drops silently because they ignore nulls anyway.\n\t// Anything else the dialect calls unsupported, and so does the port.\n\tIgnoreNullsWindowFuncs map[string]struct{}\n\tIgnoreNullsDropped     map[string]struct{}\n",
        "\t// WithinGroupPercentile is what a PERCENTILE under a WITHIN GROUP\n\t// becomes: \"outside\" keeps the clause, \"inside\" folds it into the\n\t// call and reshuffles the arguments, and empty means the dialect\n\t// writes a different function and the port refuses.\n\tWithinGroupPercentile string\n",
        "\t// JoinHints are the words a dialect allows between the KIND and the\n\t// JOIN, naming how the engine should do it: `INNER HASH JOIN`. They\n\t// are words rather than tokens, and a dialect that names none writes\n\t// none -- the reference drops a hint where the target has no hints.\n\tJoinHints map[string]struct{}\n",
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
        "\tTypeDispatchFunctions map[string]TypeDispatch\n",        "\t// ValueDispatchFunctions are names whose CLASS is chosen by the\n\t// WORD in one argument: T-SQL's HASHBYTES('SHA1', x) is an SHA and\n\t// HASHBYTES('MD5', x) an MD5. A word not listed takes Default.\n\tValueDispatchFunctions map[string]ValueDispatch\n",
        "\t// CreateProperties are the words that may stand between CREATE and\n\t// what it creates, each carrying a bare property of its own:\n\t// MATERIALIZED, UNLOGGED, TRANSIENT. The class is read off the tree,\n\t// not made from the word -- STREAMING becomes a StreamingTableProperty.\n\tCreateProperties map[string]string\n",
        "\t// KeepsUnnamedWrapped are the names whose wrapped slot keeps the\n\t// NODE where the argument has no name to take, rather than building\n\t// something else: PostgreSQL's DATE_BIN takes a subquery as its unit.\n\tKeepsUnnamedWrapped map[string]struct{}\n",
        "\t// UserDefinedTypeIsIdentifier wraps such a type's NAME in an\n\t// Identifier. PostgreSQL does and the rest keep the word as it\n\t// stands, which is a difference the dump shows and nothing else does.\n\tUserDefinedTypeIsIdentifier bool\n",
        "\t// CreateKinds are the things this dialect will CREATE. T-SQL alone\n\t// spells a procedure PROC as well as PROCEDURE.\n\tCreateKinds map[string]struct{}\n",
        "\t// CreateOrFlags are the words after CREATE OR and the flag each\n\t// turns on: REPLACE and T-SQL's ALTER both mean `replace`, and\n\t// Databricks' REFRESH means `refresh`.\n\tCreateOrFlags map[string]string\n",
        "\t// CreateFlagWords is how each of those flags is WRITTEN, which need\n\t// not be the word it was read from: T-SQL reads OR REPLACE and OR\n\t// ALTER alike and writes OR ALTER.\n\tCreateFlagWords map[string]string\n",
        "\t// ArityKindSpecs are the shapes an arity takes when one argument is\n\t// of a particular kind, beside the plain shape in FunctionsByArity.\n\tArityKindSpecs map[string]map[int][]KindSpec\n",

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
        "\t// SwappedRangeOps are the ones whose operands go the OTHER way\n\t// round: PostgreSQL's `x @@ y` is a MatchAgainst of y over x.\n\tSwappedRangeOps map[TokenType]struct{}\n",
        "\t// ListedRangeOps hold one operand as a LIST of one, which is the\n\t// reference's shape for `@@` rather than a second operand.\n\tListedRangeOps map[TokenType]struct{}\n",
        "\t// BinaryRangeSQL is how each of those is written back.\n",
        "\tBinaryRangeSQL map[string]string\n",
        "\t// JSONOperatorsAtBitwise are the operators this dialect reads\n",
        "\t// level with `||` rather than as an accessor binding tighter\n",
        "\t// than arithmetic. Probed by the asymmetry: `1 + x #> 'y'` is\n",
        "\t// `(1 + x) #> 'y'` where the operator is here, and\n",
        "\t// `1 + (x #> 'y')` where it is not.\n",
        "\tJSONOperatorsAtBitwise map[TokenType]string\n",
        "\t// JSONOperatorSQL is how each of those is written back, which\n",
        "\t// is only ever in the dialect that reads it there: elsewhere\n",
        "\t// the same node is a function call.\n",
        "\tJSONOperatorSQL map[string]string\n",
        "\t// TypeTokens maps a type keyword to the DataType.Type member the\n",
        "\t// reference records. A few type tokens have no member and are absent,\n",
        "\t// which refuses them rather than inventing one.\n",
        "\tTypeTokens map[TokenType]string\n",
        "\t// TimestampTypeTokens are the types a WITH/WITHOUT TIME ZONE may\n",
        "\t// follow, and TimeTypeTokens the ones among them that become a\n",
        "\t// TIMETZ rather than a TIMESTAMPTZ when it says WITH.\n",
        "\tTimestampTypeTokens map[TokenType]struct{}\n",
        "\tTimeTypeTokens      map[TokenType]struct{}\n",
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
        "// ValueDispatch is one name's choice of class by the WORD in an\n",
        "// argument: HASHBYTES('SHA1', x) is an SHA, HASHBYTES('MD5', x) an\n",
        "// MD5, and anything else the Default. The words are read off the\n",
        "// reference builder's own constants, not transcribed.\n",
        "// KindSpec is the shape one arity takes when the argument at Index is\n",
        "// of a particular KIND. PostgreSQL's REGEXP_REPLACE reads its last\n",
        "// argument as flags when it is a string that does not spell a number,\n",
        "// and as a position when it does -- one arity, two shapes.\n",
        "type KindSpec struct {\n",
        "\tIndex int\n",
        "\tKind  string\n",
        "\tSpec  FuncSpec\n",
        "}\n\n",
        "type ValueDispatch struct {\n",
        "\tIndex   int\n",
        "\tDefault FuncSpec\n",
        "\tByValue map[string]FuncSpec\n",
        "}\n\n",
        "type FuncSpec struct {\n",
        "\tClass string\n",
        "\tArgs  []FuncArg\n",
        "\t// Annot is the TYPE the builder annotated its OWN node with:\n",
        "\t// PostgreSQL's DIV is a Cast over an IntDiv, and the cast carries\n",
        "\t// its target twice. Nil where the builder annotates nothing.\n",
        "\tAnnot *FuncArg\n",
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
        "\t// MinArgs is how many members a LIST argument must hold for this\n\t// spelling to apply. T-SQL writes CONCAT(a, b) for two and just `a`\n\t// for one, so the call form belongs to the wider counts alone.\n\tMinArgs  int\n",
        "}\n\n",
        "// FuncConst is an argument that must hold this value for the spelling\n",
        "// beside it to apply.\n",
        "type FuncConst struct {\n",
        "\tKey   string\n",
        "\tValue any\n",
        "}\n\n",
        "// PropertySpec is one word a CREATE may say about the thing it makes:\n",
        "// the node it builds, the shape of what follows the word, and whether\n",
        "// an equals sign may stand between them.\n",
        "type PropertySpec struct {\n",
        "\tClass string\n",
        "\tShape string\n",
        "\tEquals bool\n",
        "\t// Named marks a word that takes a NAME before its list: T-SQL's\n",
        "\t// `ON b (c)` names a partition scheme and the column it splits by.\n",
        "\t// A word without it is handed the whole of `b (c)` as one call.\n",
        "\tNamed bool\n",
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
        "// TableSampleSQL is how a sampling clause is written.\n",
        "type TableSampleSQL struct {\n",
        "\tKeywords       string\n",
        "\tSeedKeyword    string\n",
        "\tWithMethod     bool\n",
        "\tSizeIsRows     bool\n",
        "\tRequiresParens bool\n",
        "\tSizeIsPercent  bool\n",
        "}\n\n",
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
        "\t// Bracketed are the keys this dialect wraps in parentheses by\n\t// PRECEDENCE where the operand is not an atom: DuckDB writes\n\t// `d + INTERVAL 1 DAY` for a literal and `d + INTERVAL (x) DAY` for\n\t// anything else. The template keeps them bare and the writer puts the\n\t// brackets back where the operand needs them.\n\tBracketed []string\n",
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
        "\t// Nested names a class built AROUND the arguments rather than\n",
        "\t// from one of them -- DuckDB's ANY_VALUE is an IgnoreNulls over\n",
        "\t// an AnyValue, and T-SQL's YEAR a Year over a TsOrDsToDate. Its\n",
        "\t// own arguments are described the same way, one level down.\n",
        "\tNested     string\n",
        "\tNestedArgs []FuncArg\n",
        "\t// NestedExcept names the argument KINDS that ESCAPE that wrapper:\n",
        "\t// \"string\", \"number\", \"cast\", \"call\", \"subquery\". T-SQL's LEN casts\n",
        "\t// its argument to TEXT unless it is ALREADY a string, so the cast\n",
        "\t// belongs in the spec and a string literal is the exception. Every\n",
        "\t// wrapper used to be unconditional, and a builder with a rule like\n",
        "\t// this one was refused as undescribable at every arity.\n",
        "\tNestedExcept []string\n",
        "\t// NestedAnnot is the TYPE the wrapper is annotated with, where the\n",
        "\t// builder annotates as well as builds: `exp.cast` records its target\n",
        "\t// on the node too, and the reference dumps that. A port that built\n",
        "\t// the cast without it produced a tree the differential called\n",
        "\t// different over a field no SQL shows.\n",
        "\tNestedAnnot *FuncArg\n",

        "}\n\n",
        "var parserTables = map[string]*ParserTables{\n",
    ]
    for name in DIALECTS:
        P = Dialect.get_or_raise(name or None).parser_class
        G = Dialect.get_or_raise(name or None).generator_class
        named = set(P.FUNCTIONS) | set(P.FUNCTION_PARSERS)
        out.append(f"\t{gostr(name)}: {{\n")
        out.append(ttset("IDVarTokens", P.ID_VAR_TOKENS))
        out.append(ttset("TableAliasTokens", P.TABLE_ALIAS_TOKENS))
        out.append(strset("NamedFunctions", named))
        out.append(
            strset(
                "CallNamesTheReferenceKnows",
                named
                | set(P.NO_PAREN_FUNCTION_PARSERS)
                | {t.name for t in P.SUBQUERY_PREDICATES},
            )
        )
        out.append(ttset("NoParenFunctions", P.NO_PAREN_FUNCTIONS))
        import sqlglot as _sg  # noqa: PLC0415

        _proc = _sg.parse_one("CREATE PROCEDURE foo AS SELECT 1", read=name or None)
        _named = _proc.args.get("this")
        out.append(
            "\t\tBareProcedureWrapper: %s,\n"
            % gostr("" if isinstance(_named, exp.Table) else type(_named).__name__)
        )
        _with = {}
        for _word in sorted(set(P.PROCEDURE_OPTIONS) | set(getattr(P, "VIEW_ATTRIBUTES", ()))):
            try:
                _one = _sg.parse_one(
                    f"CREATE PROCEDURE foo WITH {_word} AS SELECT 1", read=name or None
                )
            except Exception:  # noqa: BLE001 -- a word this dialect will not read there
                continue
            _props = _one.args.get("properties")
            _items = _props.args["expressions"] if _props is not None else []
            if len(_items) == 1:
                _with[_word] = type(_items[0]).__name__
        try:
            _defaulted = _sg.parse_one("CREATE TABLE t (a INT = 1)", read=name or None)
            _col = _defaulted.args["this"].args["expressions"][0]
            _has_default = _col.args.get("default") is not None
        except Exception:  # noqa: BLE001 -- a dialect that will not read it
            _has_default = False
        out.append(f"\t\tColumnDefaultAfterEquals: {str(_has_default).lower()},\n")
        try:
            _if = _sg.parse_one("IF 1 = 1 SELECT 1", read=name or None)
            _if_statement = type(_if).__name__ == "IfBlock"
        except Exception:  # noqa: BLE001 -- a dialect that will not read it
            _if_statement = False
        out.append(f"\t\tIfIsAStatement: {str(_if_statement).lower()},\n")
        try:
            _alt = _sg.parse_one(
                "ALTER TABLE a ALTER COLUMN b CHAR(10) COLLATE abc", read=name or None
            ).args["actions"][0]
            _collate = _alt.args.get("collate")
            _is_var = type(getattr(_collate, "this", None)).__name__ == "Var"
        except Exception:  # noqa: BLE001 -- a dialect that will not read it
            _is_var = False
        out.append(f"\t\tAlterCollateIsVar: {str(_is_var).lower()},\n")
        try:
            _nn = _sg.parse_one(
                "ALTER TABLE a ALTER COLUMN b INT NOT NULL", read=name or None
            ).args["actions"][0]
            _takes_null = _nn.args.get("allow_null") is not None
        except Exception:  # noqa: BLE001
            _takes_null = False
        out.append(f"\t\tAlterColumnTypeTakesNull: {str(_takes_null).lower()},\n")
        out.append(strset("OpclassFollowWords", P.OPCLASS_FOLLOW_KEYWORDS))
        out.append(ttset("OpclassFollowTokens", P.OPTYPE_FOLLOW_TOKENS))
        out.append(strset("TrimTypes", P.TRIM_TYPES))
        out.append(f"\t\tTrimPatternFirst: {str(bool(P.TRIM_PATTERN_FIRST)).lower()},\n")
        out.append(
            "\t\tProjectionEqualsIsAlias: "
            f"{str(projection_shape(name, 'SELECT zza = 1') == 'Alias').lower()},\n"
        )
        out.append(
            "\t\tPositionalColumns: "
            f"{str(projection_shape(name, 'SELECT #2 FROM zzt') == 'PositionalColumn').lower()},\n"
        )
        out.append(
            "\t\tAlterColumnNullabilityWritten: %s,\n"
            % str(
                bool(
                    Dialect.get_or_raise(name or None).generator_class.
                    SUPPORTS_ALTER_COLUMN_NULLABILITY
                )
            ).lower()
        )
        if _with:
            out.append("\t\tProcedureWithOptions: map[string]string{\n")
            for _word in sorted(_with):
                out.append(f"\t\t\t{gostr(_word)}: {gostr(_with[_word])},\n")
            out.append("\t\t},\n")
        out.append(opmap("NoParenFunctionClasses", P.NO_PAREN_FUNCTIONS))
        # Names with their own SYNTAX, not merely their own builder:
        # EXTRACT(unit FROM x), TRIM(BOTH ' ' FROM x), POSITION(a IN b). The
        # port used to lump these in with unportable builders, which filed
        # missing GRAMMAR under a label that reads as "cannot be ported" -- and
        # that label is what decides what gets built next.
        out.append(strset("SyntaxFunctions", sorted(P.FUNCTION_PARSERS)))
        out.append(strset("TableFunctions", table_functions(name, P)))
        _fmt_args = time_format_args(name, P, exp, list(P.FUNCTIONS))
        (
            funcs,
            by_arity,
            unit_maps,
            dispatch,
            string_wraps,
            root_annots,
            value_dispatch,
            kind_specs,
        ) = probe_functions(
            P, exp, name, format_args=_fmt_args
        )
        if string_wraps:
            out.append("\t\tStringArgWraps: map[string]map[int]string{\n")
            for fn in sorted(string_wraps):
                pairs = ", ".join(
                    f"{i}: {gostr(string_wraps[fn][i])}" for i in sorted(string_wraps[fn])
                )
                out.append(f"\t\t\t{gostr(fn)}: {{{pairs}}},\n")
            out.append("\t\t},\n")
        if dispatch:
            out.append("\t\tTypeDispatchFunctions: map[string]TypeDispatch{\n")
            for fn in sorted(dispatch):
                d = dispatch[fn]
                dcls, dspec = d["default"]
                out.append(f"\t\t\t{gostr(fn)}: {{\n")
                out.append(f"\t\t\t\tIndex: {d['index']},\n")
                out.append(
                    f"\t\t\t\tDefault: FuncSpec{{{gostr(dcls)}, "
                    f"[]FuncArg{{{funcargs(dspec)}}}, nil}},\n"
                )
                out.append("\t\t\t\tByType: map[string]FuncSpec{\n")
                for ty in sorted(d["by_type"]):
                    cls, spec = d["by_type"][ty]
                    out.append(
                        f"\t\t\t\t\t{gostr(ty)}: {{{gostr(cls)}, "
                        f"[]FuncArg{{{funcargs(spec)}}}, nil}},\n"
                    )
                out.append("\t\t\t\t},\n\t\t\t},\n")
            out.append("\t\t},\n")
        if kind_specs:
            out.append(
                "\t\tArityKindSpecs: map[string]map[int][]KindSpec{\n"
            )
            for fn in sorted(kind_specs):
                out.append(f"\t\t\t{gostr(fn)}: {{\n")
                for width in sorted(kind_specs[fn]):
                    forms = kind_specs[fn][width]
                    parts = []
                    for (i, label) in sorted(forms):
                        cls, spec = forms[(i, label)]
                        parts.append(
                            "{%d, %s, FuncSpec{%s, []FuncArg{%s}, nil}}"
                            % (i, gostr(label), gostr(cls), funcargs(spec))
                        )
                    out.append(f"\t\t\t\t{width}: {{{', '.join(parts)}}},\n")
                out.append("\t\t\t},\n")
            out.append("\t\t},\n")
        out.append(
            "\t\tUserDefinedTypeIsIdentifier: "
            f"{str(user_defined_type_is_identifier(name)).lower()},\n"
        )
        create_props, create_kinds, create_flags = create_words(name)
        replace_words = create_replace_words(name)
        out.append("\t\tCreateFlagWords: map[string]string{\n")
        for flag in sorted(replace_words):
            out.append(f"\t\t\t{gostr(flag)}: {gostr(replace_words[flag])},\n")
        out.append("\t\t},\n")
        out.append("\t\tCreateProperties: map[string]string{\n")
        for word in sorted(create_props):
            out.append(f"\t\t\t{gostr(word)}: {gostr(create_props[word])},\n")
        out.append("\t\t},\n")
        out.append(
            "\t\tCreateKinds: map[string]struct{}{"
            + "".join(f"{gostr(k)}: {{}}, " for k in create_kinds)
            + "},\n"
        )
        out.append("\t\tCreateOrFlags: map[string]string{\n")
        for word in sorted(create_flags):
            out.append(f"\t\t\t{gostr(word)}: {gostr(create_flags[word])},\n")
        out.append("\t\t},\n")
        if value_dispatch:
            out.append("\t\tValueDispatchFunctions: map[string]ValueDispatch{\n")
            for fn in sorted(value_dispatch):
                d = value_dispatch[fn]
                dcls, dspec = d["default"]
                out.append(f"\t\t\t{gostr(fn)}: {{\n")
                out.append(f"\t\t\t\tIndex: {d['index']},\n")
                out.append(
                    f"\t\t\t\tDefault: FuncSpec{{{gostr(dcls)}, "
                    f"[]FuncArg{{{funcargs(dspec)}}}, nil}},\n"
                )
                out.append("\t\t\t\tByValue: map[string]FuncSpec{\n")
                for word in sorted(d["by_value"]):
                    cls, spec = d["by_value"][word]
                    out.append(
                        f"\t\t\t\t\t{gostr(word)}: {{{gostr(cls)}, "
                        f"[]FuncArg{{{funcargs(spec)}}}, nil}},\n"
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
        out.append(strset("KeepsUnnamedWrapped", keeps_unnamed_wrapped(name, funcs)))
        out.append(funcmap("Functions", funcs, root_annots))
        if by_arity:
            out.append("\t\tFunctionsByArity: map[string]map[int]FuncSpec{\n")
            for fname in sorted(by_arity):
                out.append(f"\t\t\t{gostr(fname)}: {{\n")
                for arity in sorted(by_arity[fname]):
                    cls, spec = by_arity[fname][arity]
                    out.append(
                        f"\t\t\t\t{arity}: {{{gostr(cls)}, []FuncArg{{{funcargs(spec)}}}, "
                        f"{goannot(root_annots.get((fname, arity)))}}},\n"
                    )
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
        elided = cast_elisions(exp, name, render_forms)[0]
        elided_over = cast_over_elisions(exp, name, render_forms)
        if elided:
            out.append("\t\tCastElidedAt: map[string]map[string][]string{\n")
            for cls_key in sorted(elided):
                inner = ", ".join(
                    "%s: {%s}" % (gostr(k), ", ".join(gostr(t) for t in sorted(elided[cls_key][k])))
                    for k in sorted(elided[cls_key])
                )
                out.append(f"\t\t\t{gostr(cls_key)}: {{{inner}}},\n")
            out.append("\t\t},\n")
        if elided_over:
            out.append("\t\tCastElidedOver: map[string][]string{\n")
            for ty in sorted(elided_over):
                inner = ", ".join(gostr(c) for c in sorted(elided_over[ty]))
                out.append(f"\t\t\t{gostr(ty)}: {{{inner}}},\n")
            out.append("\t\t},\n")
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
        syn, idem = syntax_templates(exp, name, pathlib.Path(__file__).resolve().parent.parent)
        out.append("\t\tCastIdempotentTypes: map[string]map[string][]string{\n")
        for cls_key in sorted(idem):
            inner = ", ".join(
                "%s: {%s}" % (gostr(k), ", ".join(gostr(t) for t in sorted(idem[cls_key][k])))
                for k in sorted(idem[cls_key])
            )
            out.append(f"\t\t\t{gostr(cls_key)}: {{{inner}}},\n")
        out.append("\t\t},\n")
        if syn:
            out.append("\t\tSyntaxSQL: map[string][]SyntaxTemplate{\n")
            for cls_name in sorted(syn):
                entries = "".join(
                    "{[]string{%s}, []string{%s}, []FuncConst{%s}, %s, []string{%s}}, "
                    % (
                        ", ".join(gostr(k) for k in keys),
                        ", ".join(gostr(k) for k in marked),
                        "".join("{%s, %s}, " % (gostr(k), goconst(v)) for k, v in required),
                        gostr(text),
                        ", ".join(gostr(k) for k in bracketed),
                    )
                    for keys, marked, required, text, bracketed in syn[cls_name]
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
        for field, attr in (("DatePartMapping", "DATE_PART_MAPPING"),
                            ("InverseTimeMapping", "INVERSE_TIME_MAPPING"),
                            ("FormatTimeMapping", "FORMAT_TIME_MAPPING")):
            _m = getattr(_D.get_or_raise(name or None), attr, None) or {}
            if not _m:
                continue
            out.append(f"\t\t{field}: map[string]string{{\n")
            for k in sorted(_m):
                out.append(f"\t\t\t{gostr(k)}: {gostr(_m[k])},\n")
            out.append("\t\t},\n")
        _tfa = time_format_args(name, P, exp, render_input)
        if _tfa:
            out.append("\t\tTimeFormatArgs: map[string][]int{\n")
            for fname in sorted(_tfa):
                joined = ", ".join(str(i) for i in _tfa[fname])
                out.append(f"\t\t\t{gostr(fname)}: {{{joined}}},\n")
            out.append("\t\t},\n")
        casts, zeros, drops, cast_types = cast_sensitive_args(P, exp, name, render_input)
        _cc = {}
        # Both buckets: a slot may be sensitive to a CAST it carries or to
        # being a LITERAL, and the wrapper is the same question either way.
        _sensitive = {}
        for _src in (casts, zeros):
            for _cls, _by in _src.items():
                _sensitive.setdefault(_cls, set()).update(
                    k for keys in _by.values() for k in keys
                )
        for _cls in sorted(_sensitive):
            _keys = sorted(_sensitive[_cls])
            _per = cast_coercions(exp, name, _cls, _keys)
            if _per:
                _cc[_cls] = _per
        out.append(
            "\t\tCastCoercions: map[string]map[int]map[string]map[string]string{\n"
        )
        for _cls in sorted(_cc):
            per_arity = ", ".join(
                "%d: {%s}" % (
                    arity,
                    ", ".join(
                        "%s: {%s}" % (
                            gostr(k),
                            ", ".join(
                                "%s: %s" % (gostr(t), gostr(w))
                                for t, w in sorted(_cc[_cls][arity][k].items())
                            ),
                        )
                        for k in sorted(_cc[_cls][arity])
                    ),
                )
                for arity in sorted(_cc[_cls])
            )
            out.append(f"\t\t\t{gostr(_cls)}: {{{per_arity}}},\n")
        out.append("\t\t},\n")

        if cast_types:
            out.append("\t\tCastSensitiveTypes: map[string]map[int]map[string][]string{\n")
            for fname in sorted(cast_types):
                per_arity = []
                for arity in sorted(cast_types[fname]):
                    keys = cast_types[fname][arity]
                    inner = ", ".join(
                        "%s: {%s}" % (gostr(k), ", ".join(gostr(t) for t in sorted(keys[k])))
                        for k in sorted(keys)
                    )
                    per_arity.append("%d: {%s}" % (arity, inner))
                out.append(
                    f"\t\t\t{gostr(fname)}: {{{', '.join(per_arity)}}},\n"
                )
            out.append("\t\t},\n")
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
        # A position whose spec ALREADY explains what a string does there is
        # not sensitive any more, it is described: T-SQL's LEN skips its cast
        # for a string, which the wrapper's own exception records. Left in,
        # the older mechanism refused `LEN('x')` for a rule the newer one had
        # just learned.
        for fname, positions in list(sensitive.items()):
            explained = set()
            for cls_spec in [funcs.get(fname)] + [
                by_arity.get(fname, {}).get(a) for a in by_arity.get(fname, {})
            ]:
                if not cls_spec:
                    continue
                for _, how in cls_spec[1]:
                    if "string" not in (how.get("except") or ()):
                        continue
                    held = nested_index(how)
                    if held is not None:
                        explained.add(held)
            # And a position whose KIND spec says what a string does there.
            for width, forms in kind_specs.get(fname, {}).items():
                for (i, label) in forms:
                    if label == "string":
                        explained.add(i)
            kept = [i for i in positions if i not in explained]
            if kept:
                sensitive[fname] = kept
            else:
                del sensitive[fname]
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
        # The name a type takes when it carries PARAMETERS is not always the
        # one it takes bare: Databricks writes a bare VARCHAR as STRING and a
        # VARCHAR(255) as itself. Only the ones that differ are recorded.
        sized_sql = {}
        for t, member in sorted(
            {t: exp.DType[t.name] for t in P.TYPE_TOKENS if t.name in exp.DType.__members__}.items(),
            key=lambda kv: kv[0].value,
        ):
            try:
                node = exp.DataType(
                    this=member,
                    expressions=[exp.DataTypeParam(this=exp.Literal.number(10))],
                )
                rendered = node.sql(dialect=name or None)
            except Exception:  # noqa: BLE001
                continue
            head = rendered.split("(")[0]
            if head and head != types_sql.get(member.value):
                sized_sql[member.value] = head
        if sized_sql:
            out.append(strstrmap("SizedTypeSQL", sized_sql))
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
        out.append(ttset("InvalidFuncNameTokens", P.INVALID_FUNC_NAME_TOKENS))
        out.append(strset("BareNameIsColumn", bare_name_is_column(name)))
        out.append(strset("FunctionsWithAliasedArgs", P.FUNCTIONS_WITH_ALIASED_ARGS))
        struct_wrap, struct_field = struct_spelling(name)
        out.append(f"\t\tStructTemplate: {gostr(struct_wrap)},\n")
        out.append(f"\t\tStructFieldTemplate: {gostr(struct_field)},\n")
        _props = property_specs(name)
        if _props:
            out.append("\t\tPropertySpecs: map[string]PropertySpec{\n")
            for _word in sorted(_props):
                _spec = _props[_word]
                out.append(
                    f"\t\t\t{gostr(_word)}: {{{gostr(_spec['class'])}, "
                    f"{gostr(_spec['shape'])}, {str(_spec['eq']).lower()}, "
                    f"{str(_spec.get('named', False)).lower()}}},\n"
                )
            out.append("\t\t},\n")
        _gen = Dialect.get_or_raise(name or None).generator_class
        out.append("\t\tPropertyLocation: map[string]string{\n")
        for _cls, _loc in sorted(
            ((c.__name__, v.name) for c, v in _gen.PROPERTIES_LOCATION.items())
        ):
            out.append(f"\t\t\t{gostr(_cls)}: {gostr(_loc)},\n")
        out.append("\t\t},\n")
        out.append(f"\t\tWithPropertiesPrefix: {gostr(_gen.WITH_PROPERTIES_PREFIX)},\n")
        _pn = exp.Property(this=exp.var("kkk"), value=exp.Literal.number(5)).sql(
            dialect=name or None
        )
        out.append(f"\t\tPropertyNameQuoted: {str(_pn.startswith(chr(39))).lower()},\n")
        reads_colon, colon_template = colon_lambda(name)
        out.append(f"\t\tColonLambdaRead: {str(reads_colon).lower()},\n")
        if colon_template:
            out.append(f"\t\tColonLambdaWrite: {gostr(colon_template)},\n")
        d = Dialect.get_or_raise(name or None)
        for field, value in (
            ("TypedDivision", d.TYPED_DIVISION),
            ("SafeDivision", d.SAFE_DIVISION),
            ("DPipeIsStringConcat", d.DPIPE_IS_STRING_CONCAT),
            ("StrictStringConcat", d.STRICT_STRING_CONCAT),
            ("JoinsHaveEqualPrecedence", P.JOINS_HAVE_EQUAL_PRECEDENCE),
            ("ValuesFollowedByParen", P.VALUES_FOLLOWED_BY_PAREN),
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
        from sqlglot.dialects.dialect import Dialect as _DG

        out.append(strset("TsOrDsParents", strips_ts_or_ds(name)))
        out.append(f"\t\tPrefixAlias: {str(prefix_alias(name)).lower()},\n")
        _gen = type(_DG.get_or_raise(name or None)).generator_class
        out.append(
            "\t\tLikeInsideSchema: %s,\n"
            % str(bool(getattr(_gen, "LIKE_PROPERTY_INSIDE_SCHEMA", False))).lower()
        )
        out.append(
            "\t\tSupportsCreateLike: %s,\n"
            % str(bool(getattr(_gen, "SUPPORTS_CREATE_TABLE_LIKE", True))).lower()
        )
        out.append("\t\tEndCommits: %s,\n" % str(end_commits(name)).lower())
        # Only the classes a TIME FORMAT argument actually builds. Plenty of
        # other classes carry an argument called `format` -- a COPY's, a
        # DESCRIBE's -- and it is not a time format at all.
        _fmt_classes = set()
        for fname in _fmt_args:
            if fname in funcs:
                _fmt_classes.add(funcs[fname][0])
            for _, (cls_name, _spec) in by_arity.get(fname, {}).items():
                _fmt_classes.add(cls_name)
        _ifc = inverse_format_classes(name, exp, _fmt_classes)
        body = "".join(f"\t\t\t{gostr(k)}: {gostr(v)},\n" for k, v in sorted(_ifc))
        out.append(f"\t\tFormatSpellings: map[string]string{{\n{body}\t\t}},\n")
        from sqlglot.time import format_time as _ft
        _D = _DG.get_or_raise(name or None)
        out.append(
            "\t\tDefaultTimeFormat: %s,\n"
            % gostr(_ft(str(getattr(_D, "TIME_FORMAT", "") or "").strip("'"),
                        getattr(_D, "TIME_MAPPING", None) or {}))
        )
        _pc = prefix_calls(name, P)
        body = "".join(f"\t\t\t{gostr(k)}: {gostr(v)},\n" for k, v in sorted(_pc.items()))
        out.append(f"\t\tPrefixCalls: map[string]string{{\n{body}\t\t}},\n")
        out.append(
            "\t\tExecuteBuildsExecute: %s,\n" % str(execute_builds_execute(name)).lower()
        )
        out.append(strset("ShowKinds", sorted(P.SHOW_PARSERS)))
        toks = sorted(P.RESERVED_TOKENS, key=lambda t: t.value)
        body = "".join(f"\t\t\tTok{t.name}: {{}},\n" for t in toks)
        out.append(f"\t\tReservedTokens: map[TokenType]struct{{}}{{\n{body}\t\t}},\n")
        body = "".join(
            "\t\t\t%s: {%s},\n" % (gostr(k), ", ".join(gostr(w) for w in v))
            for k, v in sorted(P.CREATE_SEQUENCE.items())
        )
        out.append(f"\t\tSequenceOptions: map[string][]string{{\n{body}\t\t}},\n")
        toks = sorted(P.CREATABLES, key=lambda t: t.value)
        body = "".join(f"\t\t\tTok{t.name}: {{}},\n" for t in toks)
        out.append(f"\t\tCreatableTokens: map[TokenType]struct{{}}{{\n{body}\t\t}},\n")
        _ckm = getattr(_DG.get_or_raise(name or None), "CREATABLE_KIND_MAPPING", None) or {}
        body = "".join(f"\t\t\t{gostr(k)}: {gostr(v)},\n" for k, v in sorted(_ckm.items()))
        out.append(f"\t\tCreatableKindNames: map[string]string{{\n{body}\t\t}},\n")
        out.append(
            "\t\tCopyParamsAreCSV: %s,\n"
            % str(bool(getattr(type(_DG.get_or_raise(name or None)), "COPY_PARAMS_ARE_CSV", True))).lower()
        )
        out.append(
            "\t\tCopyIntoWritten: %s,\n"
            % str(bool(getattr(type(_DG.get_or_raise(name or None)).generator_class,
                               "COPY_HAS_INTO_KEYWORD", True))).lower()
        )
        out.append(
            "\t\tCopyParamsWrapped: %s,\n"
            % str(bool(getattr(type(_DG.get_or_raise(name or None)).generator_class,
                               "COPY_PARAMS_ARE_WRAPPED", True))).lower()
        )
        out.append(
            "\t\tCopyParamsNeedEQ: %s,\n"
            % str(bool(getattr(type(_DG.get_or_raise(name or None)).generator_class,
                               "COPY_PARAMS_EQ_REQUIRED", False))).lower()
        )
        out.append(strset("CopyVarlenOptions", sorted(P.COPY_INTO_VARLEN_OPTIONS)))
        _uo = unary_ops(name, P, exp)
        body = "".join(f"\t\t\tTok{t}: {gostr(c)},\n" for t, c in sorted(_uo.items()))
        out.append(f"\t\tUnaryOps: map[TokenType]string{{\n{body}\t\t}},\n")
        out.append(
            "\t\tDeclareAssignment: %s,\n"
            % gostr(getattr(type(_DG.get_or_raise(name or None)).generator_class,
                            "DECLARE_DEFAULT_ASSIGNMENT", "="))
        )
        out.append(
            "\t\tStarExceptWord: %s,\n"
            % gostr(getattr(type(_DG.get_or_raise(name or None)).generator_class,
                            "STAR_EXCEPT", "EXCEPT"))
        )
        out.append(
            "\t\tNormalizeNotNull: %s,\n"
            % str(
                bool(getattr(type(_DG.get_or_raise(name or None)), "NORMALIZE_NOT_NULL", False))
            ).lower()
        )
        out.append(
            f"\t\tConvertBuildsConvert: {str(convert_builds_convert(name)).lower()},\n"
        )
        _wgf = within_group_folding_names(name, P)
        if _wgf:
            out.append(strset("WithinGroupFolds", _wgf))
        out.append(f"\t\tMapBraceLiteral: {str(map_brace_literal(name)).lower()},\n")
        out.append(
            "\t\tHoistsInsertWith: %s,\n" % str(hoists_insert_with(name)).lower()
        )
        out.append(
            "\t\tGeneratedExpressionIsComputed: %s,\n"
            % str(generated_expression_is_computed(name)).lower()
        )
        _scs = string_class_sql(name)
        out.append("\t\tStringClassSQL: map[string]string{\n")
        for cls, spec in sorted(_scs.items()):
            out.append(f"\t\t\t{gostr(cls)}: {gostr(spec)},\n")
        out.append("\t\t},\n")
        _iua = interval_unit_aliases(name)
        out.append("\t\tIntervalUnitAliases: map[string]string{\n")
        for key, value in sorted(_iua.items()):
            out.append(f"\t\t\t{gostr(key)}: {gostr(value)},\n")
        out.append("\t\t},\n")
        out.append("\t\tTableHintsWritten: %s,\n" % str(table_hints_written(name)).lower())
        out.append(
            "\t\tBareBeginIsATransaction: %s,\n"
            % str(bare_begin_is_a_transaction(name)).lower()
        )
        _tw, _ts, _tn = transaction_conventions(name)
        out.append(f"\t\tTransactionWord: {gostr(_tw)},\n")
        out.append("\t\tTransactionSavepointWritten: %s,\n" % str(_ts).lower())
        out.append("\t\tTransactionNameWritten: %s,\n" % str(_tn).lower())
        out.append(f"\t\tOffsetRowsWord: {gostr(offset_rows_word(name))},\n")
        out.append(f"\t\tIndexOnWord: {gostr(index_on_word(name))},\n")
        out.append(f"\t\tPartitionSQL: {gostr(partition_sql(name))},\n")
        out.append("\t\tWithDataWritten: %s,\n" % str(with_data_written(name)).lower())
        out.append(f"\t\tEnumTypeSQL: {gostr(enum_type_sql(name))},\n")
        out.append(f"\t\tIdentityShortSQL: {gostr(identity_short_sql(name))},\n")
        out.append(f"\t\tAutoIncrementSQL: {gostr(auto_increment_sql(name))},\n")
        out.append("\t\tValuesTableWrapped: %s,\n" % str(values_table_wrapped(name)).lower())
        out.append("\t\tKeyConstraintOptions: map[string][]string{\n")
        for word, follows in sorted(key_constraint_options(name).items()):
            inner = ", ".join(gostr(f) for f in follows)
            out.append(f"\t\t\t{gostr(word)}: {{{inner}}},\n")
        out.append("\t\t},\n")
        _aso, _asw = alter_set_conventions(name)
        out.append("\t\tAlterSetOptionWritten: %s,\n" % str(_aso).lower())
        out.append("\t\tAlterSetWrapsOptions: %s,\n" % str(_asw).lower())
        out.append("\t\tAlterSetListIsSettings: %s,\n" % str(alter_set_list_is_settings(name)).lower())
        try:
            _set = _sg.parse_one(
                "ALTER TABLE t SET (DATA_DELETION=ON)", read=name or None
            ).args["actions"][0]
            _wrapped = any(
                type(x).__name__ == "Properties" for x in (_set.args.get("expressions") or [])
            )
        except Exception:  # noqa: BLE001 -- a dialect that will not read it
            _wrapped = False
        out.append(f"\t\tAlterSetIsWrapped: {str(_wrapped).lower()},\n")
        _ccs, _cct = computed_column_spelling(name)
        out.append(f"\t\tComputedColumnSpelling: {gostr(_ccs)},\n")
        out.append("\t\tComputedKeepsType: %s,\n" % str(_cct).lower())
        out.append("\t\tIdentityWritten: %s,\n" % str(identity_written(name)).lower())
        out.append(
            "\t\tIdentityWidensType: %s,\n" % str(identity_widens_type(name)).lower()
        )
        out.append(f"\t\tSetItemSeparator: {gostr(set_item_separator(name))},\n")
        out.append("\t\tSetWithoutASign: %s,\n" % str(set_without_a_sign(name)).lower())
        out.append(f"\t\tSetItemVariableSeparator: {gostr(set_variable_separator(name))},\n")
        out.append("\t\tSetItemKindWritten: map[string]bool{\n")
        for word, ok in sorted(set_item_kind_written(name).items()):
            out.append(f"\t\t\t{gostr(word)}: {str(ok).lower()},\n")
        out.append("\t\t},\n")

        _pmp, _pmw = parameter_mode(name)
        out.append("\t\tParameterModePrefix: %s,\n" % str(_pmp).lower())
        out.append("\t\tParameterModeWords: map[string]string{\n")
        for key, word in sorted(_pmw.items()):
            out.append(f"\t\t\t{gostr(key)}: {gostr(word)},\n")
        out.append("\t\t},\n")
        out.append("\t\tFunctionReturnsPlace: map[string]string{\n")
        for shape, place in sorted(function_returns_place(name).items()):
            out.append(f"\t\t\t{gostr(shape)}: {gostr(place)},\n")
        out.append("\t\t},\n")
        out.append(
            "\t\tFunctionPropertiesWritten: %s,\n"
            % str(function_properties_written(name)).lower()
        )
        out.append("\t\tFunctionReturnAs: %s,\n" % str(function_return_as(name)).lower())
        out.append(
            "\t\tFunctionWrapsTableBody: %s,\n"
            % str(function_wraps_table_body(name)).lower()
        )
        out.append(
            "\t\tFunctionAsTableRead: %s,\n" % str(function_as_table_read(name)).lower()
        )
        out.append(f"\t\tReturnWord: {gostr(return_word(name))},\n")
        out.append(
            "\t\tPrimaryKeyMembersOrdered: %s,\n"
            % str(primary_key_members_ordered(name)).lower()
        )
        out.append(
            "\t\tUniqueConstraintWritten: %s,\n"
            % str(unique_constraint_written(name)).lower()
        )
        _acw, _ara = alter_add_conventions(name)
        out.append("\t\tAlterAddColumnWord: %s,\n" % str(_acw).lower())
        out.append("\t\tAlterRepeatsAdd: %s,\n" % str(_ara).lower())
        out.append(
            f"\t\tAlterColumnTypeWord: {gostr(alter_column_type_word(name))},\n"
        )
        for field, table in (
            ("CreateExistsWritten", create_exists_written(name)),
            ("TemporaryWritten", temporary_written(name)),
        ):
            out.append("\t\t%s: map[string]bool{\n" % field)
            for kind, ok in sorted(table.items()):
                out.append(f"\t\t\t{gostr(kind)}: {str(ok).lower()},\n")
            out.append("\t\t},\n")
        out.append(
            "\t\tViewColumnCommentWritten: %s,\n"
            % str(view_column_comment_written(name)).lower()
        )
        out.append(
            "\t\tMergeWithoutTarget: %s,\n" % str(merge_without_target(name)).lower()
        )
        _nu, _nq = identifier_normalization(name)
        out.append(f"\t\tNormalizeUnquoted: {gostr(_nu)},\n")
        out.append(f"\t\tNormalizeQuoted: {gostr(_nq)},\n")
        _rw, _re = returning_conventions(name)
        out.append(f"\t\tReturningWord: {gostr(_rw)},\n")
        out.append("\t\tReturningEnd: %s,\n" % str(_re).lower())
        out.append(f"\t\tRenameTarget: {gostr(rename_target(name))},\n")
        out.append(
            "\t\tTruncatesCatalog: %s,\n"
            % str(truncates_catalog(name)).lower()
        )
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
        out.append(strset("JoinHints", P.JOIN_HINTS))
        out.append("\t\tWithinGroupInside: %s,\n" % str(within_group_inside(name)).lower())
        out.append(f"\t\tWithinGroupPercentile: {gostr(within_group_percentile(name))},\n")
        out.append("\t\tIgnoreNullsInFunc: %s,\n" % str(bool(G.IGNORE_NULLS_IN_FUNC)).lower())
        out.append(
            strset(
                "IgnoreNullsWindowFuncs",
                [
                    c.__name__
                    for c in getattr(G, "IGNORE_RESPECT_NULLS_WINDOW_FUNCTIONS", ())
                ],
            )
        )
        out.append(strset("IgnoreNullsDropped", ignore_nulls_dropped(name)))
        out.append(
            "\t\tCommaUnnestJoins: %s,\n" % str(comma_unnest_joins(name)).lower()
        )
        out.append(
            "\t\tColumnCommentWritten: %s,\n" % str(column_comment_written(name)).lower()
        )
        out.append(
            "\t\tDateDeltaIsAnOperator: %s,\n"
            % str(date_delta_is_an_operator(name)).lower()
        )
        out.append(f"\t\tFileFormatSQL: {gostr(file_format_sql(name))},\n")
        out.append(
            f"\t\tSplitPartCountsBackwards: {gostr(split_part_backwards(name))},\n"
        )
        _ts = temporary_suffix(name)
        out.append("\t\tTemporarySuffix: map[string]string{\n")
        for _k in sorted(_ts):
            out.append(f"\t\t\t{gostr(_k)}: {gostr(_ts[_k])},\n")
        out.append("\t\t},\n")
        out.append(f"\t\tConditionCoercion: {gostr(condition_coercion(name))},\n")
        out.append("\t\tTableSample: TableSampleSQL{\n")
        for field, value in (
            ("Keywords", gostr(getattr(G, "TABLESAMPLE_KEYWORDS", "TABLESAMPLE"))),
            ("SeedKeyword", gostr(getattr(G, "TABLESAMPLE_SEED_KEYWORD", "SEED"))),
            ("WithMethod", str(bool(getattr(G, "TABLESAMPLE_WITH_METHOD", True))).lower()),
            ("SizeIsRows", str(bool(getattr(G, "TABLESAMPLE_SIZE_IS_ROWS", True))).lower()),
            ("RequiresParens", str(bool(getattr(G, "TABLESAMPLE_REQUIRES_PARENS", True))).lower()),
            (
                "SizeIsPercent",
                str(bool(getattr(_DG.get_or_raise(name or None), "TABLESAMPLE_SIZE_IS_PERCENT", False))).lower(),
            ),
        ):
            out.append(f"\t\t\t{field}: {value},\n")
        out.append("\t\t},\n")
        _jet = json_extract_twice(name)
        out.append("\t\tJSONExtractTwiceSQL: map[string]string{\n")
        for cls_key in sorted(_jet):
            out.append(f"\t\t\t{gostr(cls_key)}: {gostr(_jet[cls_key])},\n")
        out.append("\t\t},\n")
        _rfa = regexp_flag_args(name)
        out.append("\t\tRegexpFlagArgs: map[string]string{\n")
        for cls_key in sorted(_rfa):
            out.append(f"\t\t\t{gostr(cls_key)}: {gostr(_rfa[cls_key])},\n")
        out.append("\t\t},\n")
        flags, written, need_literal = regexp_flags(name)
        out.append(f"\t\tRegexpFlags: {gostr(flags)},\n")
        out.append("\t\tRegexpFlagsWritten: %s,\n" % str(written).lower())
        out.append("\t\tRegexpFlagsNeedLiteral: %s,\n" % str(need_literal).lower())
        from sqlglot import exp as _exp

        out.append(strset("PercentileClasses", [c.__name__ for c in _exp.PERCENTILES]))
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
        body = "".join(
            f"\t\t\tTok{k}: {{}},\n" for k, v in sorted(_br.items()) if v["swapped"]
        )
        out.append(
            f"\t\tSwappedRangeOps: map[TokenType]struct{{}}{{\n{body}\t\t}},\n"
        )
        body = "".join(
            f"\t\t\tTok{k}: {{}},\n" for k, v in sorted(_br.items()) if v["listed"]
        )
        out.append(
            f"\t\tListedRangeOps: map[TokenType]struct{{}}{{\n{body}\t\t}},\n"
        )
        spellings = {v["class"]: v["op"] for v in _br.values() if v["op"]}
        body = "".join(f"\t\t\t{gostr(k)}: {gostr(v)},\n" for k, v in sorted(spellings.items()))
        out.append(f"\t\tBinaryRangeSQL: map[string]string{{\n{body}\t\t}},\n")
        _jb = json_operators_at_bitwise(name)
        body = "".join(
            f"\t\t\tTok{k}: {gostr(v[0])},\n" for k, v in sorted(_jb.items())
        )
        out.append(
            f"\t\tJSONOperatorsAtBitwise: map[TokenType]string{{\n{body}\t\t}},\n"
        )
        body = "".join(
            f"\t\t\t{gostr(v[0])}: {gostr(v[1])},\n"
            for v in sorted(_jb.values())
        )
        out.append(f"\t\tJSONOperatorSQL: map[string]string{{\n{body}\t\t}},\n")
        types = {t: exp.DType[t.name] for t in P.TYPE_TOKENS if t.name in exp.DType.__members__}
        body = "".join(
            f"\t\t\tTok{t.name}: {gostr(v.value)},\n"
            for t, v in sorted(types.items(), key=lambda kv: kv[0].value)
        )
        out.append(f"\t\tTypeTokens: map[TokenType]string{{\n{body}\t\t}},\n")
        for field, attr in (("TimestampTypeTokens", "TIMESTAMPS"), ("TimeTypeTokens", "TIMES")):
            toks = sorted(getattr(P, attr, ()) or (), key=lambda t: t.value)
            body = "".join(f"\t\t\tTok{t.name}: {{}},\n" for t in toks)
            out.append(f"\t\t{field}: map[TokenType]struct{{}}{{\n{body}\t\t}},\n")
        out.append("\t},\n")
    out.append("}\n")

    a.out.write_text("".join(out))
    gofmt(a.out)
    print(f"reference {actual[:12]}: wrote {a.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
