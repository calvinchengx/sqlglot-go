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
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


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


def probe_functions(P, exp):
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
            else:
                return None
        return out

    def rebuild(spec, args):
        out = {}
        for key, how in spec:
            if "index" in how:
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
            elif value is not actual:
                return False
        return True

    out = {}
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
                node = builder(list(args))
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
        # The same spec must explain a call with fewer arguments.
        ok = True
        for n in range(len(args)):
            fewer = placeholders(n)
            try:
                other = builder(list(fewer))
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


def funcmap(name: str, funcs) -> str:
    lines = []
    for fn in sorted(funcs):
        cls, spec = funcs[fn]
        parts = []
        for key, how in spec:
            if "index" in how:
                parts.append(f"{{{gostr(key)}, {how['index']}, false, nil}}")
            elif "varlen" in how:
                parts.append(f"{{{gostr(key)}, {how['varlen']}, true, nil}}")
            else:
                parts.append(f"{{{gostr(key)}, -1, false, {goconst(how['const'])}}}")
        body = ", ".join(parts)
        lines.append(f"\t\t\t{gostr(fn)}: {{{gostr(cls)}, []FuncArg{{{body}}}}},\n")
    return f"\t\t{name}: map[string]FuncSpec{{\n{''.join(lines)}\t\t}},\n"


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
        "\t// IsNotNullWrapsInNot says how `x IS NOT NULL` is SHAPED. Every\n",
        "\t// dialect but PostgreSQL builds Not(Is(x, NULL)) and writes it back\n",
        "\t// as `NOT x IS NULL`; PostgreSQL records the negation on the Is\n",
        "\t// node instead. PROBED, not transcribed -- sqlglot has no flag for\n",
        "\t// it, the rule lives in a dialect override, and a port that assumed\n",
        "\t// one shape diverged on one of the commonest predicates in SQL.\n",
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
        "type FuncArg struct {\n",
        "\tKey    string\n",
        "\tIndex  int\n",
        "\tVarLen bool\n",
        "\tConst  any\n",
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
        funcs = probe_functions(P, exp)
        out.append(funcmap("Functions", funcs))
        out.append(sqlmap("FunctionSQL", render_functions(P, exp, name, funcs)))
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
    print(f"reference {actual[:12]}: wrote {a.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
