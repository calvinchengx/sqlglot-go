"""Record what the reference's type ANNOTATOR answers, as testdata.

    python harness/gen_annotate.py --sqlglot ~/opensource/sqlglot

sqlglot's annotate_types is 1,112 lines and leans on the optimizer's scope
machinery to resolve a column to a table to a schema. The port has none of
that -- but 56 of the 71 cases in the reference's own annotate_types fixture
need no column at all: a literal has a type, a cast asserts one, an operator
combines the two it is given. That subset is what the port's refusals are
waiting on. `REGEXP_REPLACE` refuses today because deciding whether its last
argument is text means asking the annotator, and for a literal the answer is
local.

So the scope-free cases are recorded here with the reference's answer, the
port is measured against them, and anything needing a column is left out
rather than guessed. Same shape as every other corpus in this repo: the
expectations are committed, so the Go tests need no Python.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys


def load_pairs(sqlglot_dir: pathlib.Path):
    import importlib.util

    spec = importlib.util.spec_from_file_location("h", sqlglot_dir / "tests/helpers.py")
    helpers = importlib.util.module_from_spec(spec)
    sys.modules["h"] = helpers
    spec.loader.exec_module(helpers)
    # Both halves of the contract. annotate_types.sql pins the OPERATORS -- a
    # literal, a cast, a negation -- and annotate_functions.sql pins what each
    # FUNCTION returns, which is the larger half by an order of magnitude and
    # is where the port's type-dependent refusals actually wait.
    out = []
    for name in ("annotate_types", "annotate_functions"):
        out.extend(helpers.load_sql_fixture_pairs(f"optimizer/{name}.sql"))
    return out


def classify_returns(sqlglot, exp, dialects):
    """What each function RETURNS, as a rule rather than an answer.

    A return type is usually not fixed: ABS(1) is an INT and ABS(1.5) a
    DOUBLE. So the probe runs each class three times -- over an integer, a
    real and a string -- and reads the RULE out of how the answers move:

      fixed    the same type whatever it was given (ASCII is always INT)
      args     the coercion of its arguments (ABS, GREATEST)
      promote  the arguments, widened (INT becomes BIGINT, FLOAT DOUBLE)
      array    the arguments, wrapped in an ARRAY

    Recording the three ANSWERS instead would be a table that is right for
    the arguments probed and wrong for every other call.
    """
    from sqlglot.optimizer.annotate_types import annotate_types

    probes = {
        "INT": lambda: exp.Literal.number(1),
        "DOUBLE": lambda: exp.Literal.number(1.5),
        "VARCHAR": lambda: exp.Literal.string("a"),
    }
    # Shapes that are NOT scalar literals, used to check a rule rather than to
    # derive one. A rule read from scalars alone was wrong for the classes that
    # look INTO their argument: ELEMENT_AT over an ARRAY<INT> returns INT, not
    # ARRAY<INT>, and ALL over a subquery is not the BOOLEAN it is over a
    # value. Both were recorded confidently and both were wrong, so a class
    # whose rule does not survive these is not recorded at all.
    checks = [
        lambda: exp.Array(expressions=[exp.Literal.number(1)]),
        lambda: exp.Subquery(this=exp.select(exp.Literal.number(1))),
    ]
    out = {}
    for dialect in sorted(dialects):
        d = dialect or None
        # What each probe's OWN type renders as here. DuckDB spells VARCHAR
        # `TEXT` and PostgreSQL spells DOUBLE `DOUBLE PRECISION`, so comparing
        # an answer against the neutral name classified `args` as `fixed` in
        # every dialect but the neutral one -- 32 rules in one and none in the
        # rest, which is what gave it away.
        baseline = {
            name: annotate_types(make(), dialect=d).type.sql(d) for name, make in probes.items()
        }
        per_class = {}
        for cls in sorted(_annotatable(exp), key=lambda c: c.__name__):
            # An unrecognised call gives NO answer rather than a confident
            # UNKNOWN. The reference does answer UNKNOWN, but for the port
            # that would be a claim about a node it did not understand: it
            # reads `ALL(SELECT 1)` as an anonymous call, where the reference
            # reads a quantifier, and an UNKNOWN there hides a PARSE gap
            # behind an annotator answer. No answer keeps the two apart.
            if cls.__name__ == "Anonymous":
                continue
            answers, kinds = {}, {}
            for name, make in probes.items():
                node = _build_call(cls, make)
                if node is None:
                    break
                try:
                    typed = annotate_types(node, dialect=d)
                except Exception:  # noqa: BLE001 -- a class this probe cannot build
                    break
                if typed.type is None:
                    break
                # Both spellings are kept. The RENDERED one classifies the
                # rule, because that is what the baseline is in; the CANONICAL
                # one is what gets recorded, because a fixed type is a type
                # and not a dialect's way of writing it. Recording the render
                # put DOUBLE PRECISION and TEXT in the table where the
                # reference has DOUBLE and VARCHAR, and the trees said so once
                # anything reached those nodes.
                answers[name] = typed.type.sql(d)
                kind = getattr(typed.type.this, "value", None)
                if not isinstance(kind, str):
                    break
                kinds[name] = kind
            if len(answers) != len(probes):
                continue
            rule = _rule_from(answers, kinds, baseline)
            if rule and _survives(cls, rule, checks, baseline, annotate_types, d, exp):
                per_class[cls.__name__] = rule
        out[dialect] = per_class
    return out


def _survives(cls, rule, checks, baseline, annotate_types, d, exp):
    """Does the rule still hold when the argument is not a scalar literal?

    And does it still hold when the arguments this probe does NOT fill are
    filled too? `If` keeps its branches under `true` and `false`, so a probe
    that sets only `this` reads the type of the CONDITION and calls it the
    rule -- which recorded `IF(true, 1, NULL)` as UNKNOWN where the reference
    says INT. Filling the other arguments and re-checking catches that,
    without dropping classes whose extra arguments are mere FLAGS: setting
    those changes nothing, so the rule survives.
    """
    arg_types = getattr(cls, "arg_types", None) or {}
    others = [k for k in arg_types if k not in ("this", "expressions")]
    if others:
        node = _build_call(cls, lambda: exp.Literal.number(1))
        if node is not None:
            for k in others:
                try:
                    node.set(k, exp.Literal.number(1))
                except Exception:  # noqa: BLE001
                    return False
            try:
                typed = annotate_types(node, dialect=d)
                got = typed.type.sql(d) if typed.type is not None else None
            except Exception:  # noqa: BLE001
                return False
            want = rule["rendered"] if rule["kind"] == "fixed" else baseline["INT"]
            if rule["kind"] in ("fixed", "args") and got != want:
                return False

    for make in checks:
        node = _build_call(cls, make)
        if node is None:
            continue
        try:
            typed = annotate_types(node, dialect=d)
        except Exception:  # noqa: BLE001
            return False
        if typed.type is None:
            return False
        got = typed.type.sql(d)
        want = annotate_types(make(), dialect=d).type
        if rule["kind"] == "fixed":
            if got != rule["rendered"]:
                return False
        elif rule["kind"] == "args":
            if want is None or got != want.sql(d):
                return False
        else:
            # promote and array are only claimed from the scalar probes; a
            # non-scalar argument is not evidence either way for them.
            continue
    return True


def _annotatable(exp):
    """The expression classes the annotator has a rule for."""
    from sqlglot.dialects.dialect import Dialect

    return [c for c in Dialect().EXPRESSION_METADATA if isinstance(c, type)]


def _build_call(cls, make):
    """One node of `cls` whose every argument is a probe literal.

    Only classes whose arguments this probe can actually FILL are built. `If`
    keeps its branches under `true` and `false`, so a probe that sets only
    `this` is reading the type of the CONDITION and calling it the rule --
    which recorded `IF(true, 1, NULL)` as UNKNOWN where the reference says
    INT. A class the probe cannot fill is not probed at all.
    """
    arg_types = getattr(cls, "arg_types", None)
    if not arg_types or "this" not in arg_types:
        return None

    try:
        args = {"this": make()}
        if "expressions" in arg_types:
            args["expressions"] = [make()]
        return cls(**args)
    except Exception:  # noqa: BLE001
        return None


def _rule_from(answers, kinds, baseline):
    """Read the rule out of three answers, against this dialect's spellings."""
    values = set(answers.values())
    if len(values) == 1:
        return {"kind": "fixed", "type": kinds["INT"], "rendered": answers["INT"]}
    if all(answers[k] == baseline[k] for k in answers):
        return {"kind": "args"}
    if answers["INT"] == "BIGINT" and answers["DOUBLE"] == baseline["DOUBLE"]:
        return {"kind": "promote"}
    if all(answers[k].startswith("ARRAY") and baseline[k] in answers[k] for k in answers):
        return {"kind": "array"}
    # Anything else moves with its arguments in a way these three probes did
    # not pin down. Recording it would be recording a guess.
    return None


def unannotated_classes(dialects) -> dict[str, list[str]]:
    """Classes the reference's annotator has no entry for at all.

    Its answer for one of those is not a guess and not a gap: it looks the
    class up in EXPRESSION_METADATA, finds nothing, and sets UNKNOWN. So does
    the port, which until now had no answer for them -- and no answer means a
    subscript over one cannot be shifted, because the shift is only allowed
    over a base that is UNKNOWN or an ARRAY and the port could not tell which
    it had.

    Anonymous is left out for the reason it is left out of the probe above:
    the port reads some calls anonymously that the reference reads as nodes of
    their own, and an UNKNOWN there would hide a PARSE gap behind an
    annotator's answer.
    """
    from sqlglot import exp
    from sqlglot.dialects.dialect import Dialect

    out = {}
    for dialect in sorted(dialects):
        metadata = Dialect.get_or_raise(dialect or None).EXPRESSION_METADATA
        named = {c.__name__ for c in metadata if isinstance(c, type)}
        out[dialect] = sorted(
            name
            for name, cls in vars(exp).items()
            if isinstance(cls, type)
            and issubclass(cls, exp.Expr)
            and name not in named
            and name != "Anonymous"
        )
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--out", default="testdata/annotate.json", type=pathlib.Path)
    a = ap.parse_args()
    sys.path.insert(0, str(a.sqlglot))

    import sqlglot
    from sqlglot import exp

    # The dialects the executor configures. The fixture also carries BigQuery
    # cases -- BIGNUMERIC, FLOAT64, ARRAY<STRING> -- and a type the port has no
    # dialect for is not a gap in the port.
    OURS = {"", "tsql", "postgres", "duckdb", "databricks"}

    cases = []
    skipped = 0
    for meta, sql, expected in load_pairs(a.sqlglot):
        # A fixture case may name SEVERAL dialects -- "hive, spark2, spark,
        # databricks" -- and the reference runs it as each. Only the ones this
        # port configures are kept, and the first of those is what it is
        # recorded as.
        named = [d.strip() for d in (meta.get("dialect") or "").split(",")]
        ours = [d for d in named if d in OURS]
        if not ours:
            skipped += 1
            continue
        dialect = ours[0]
        try:
            tree = sqlglot.parse_one(sql, read=dialect or None)
        except Exception:  # noqa: BLE001 -- the reference cannot read it either
            skipped += 1
            continue
        if any(True for _ in tree.find_all(exp.Column)):
            # Needs a column resolved to a table to a schema: that is the
            # optimizer's scope machinery, which is not ported.
            skipped += 1
            continue
        # The reference compares RENDERED types, so `bool` and `BOOLEAN` are
        # the same answer. Record the canonical form rather than the fixture's
        # spelling, or the port would be held to the spelling.
        canonical = exp.DataType.build(expected, dialect=dialect or None).sql(dialect or None)
        cases.append({"sql": sql, "dialect": dialect, "type": canonical})

    # COERCES_TO is the annotator's own table -- which types widen into which
    # -- and it is DATA rather than a closure, so it is emitted rather than
    # probed. `_maybe_coerce(a, b)` returns b when a coerces into b, and a
    # otherwise; that is the whole of binary type resolution.
    from sqlglot.optimizer.annotate_types import TypeAnnotator

    coerces = {
        k.value: sorted(v.value for v in vs)
        for k, vs in TypeAnnotator.COERCES_TO.items()
    }

    a.out.write_text(
        json.dumps({"cases": cases, "coerces_to": coerces}, indent=1, sort_keys=True) + "\n"
    )

    # ... and as a Go table, because the annotator needs it at RUN time and
    # nothing in sqlglot/ reads testdata.
    returns = classify_returns(sqlglot, exp, OURS)

    from sqlglot.dialects.dialect import Dialect

    # Whether a dialect HAS a null type. Where it does not, the reference
    # converts every NULL-typed node to UNKNOWN once inference finishes;
    # where it does -- Databricks, which spells it VOID -- the type survives.
    supports_null = {
        d: bool(Dialect.get_or_raise(d or None).SUPPORTS_NULL_TYPE) for d in sorted(OURS)
    }

    go = [
        "// Code generated by harness/gen_annotate.py from the pinned reference. DO NOT EDIT.\n",
        "//\n",
        "// Which types widen into which. The annotator resolves a binary\n",
        "// operator by asking whether one side coerces into the other; this is\n",
        "// the reference's own COERCES_TO, which is a table rather than a\n",
        "// closure and so is emitted rather than probed.\n",
        "\npackage sqlglot\n\n",
        "var coercesTo = map[string]map[string]bool{\n",
    ]
    for src in sorted(coerces):
        go.append(f'\t"{src}": {{\n')
        for dst in coerces[src]:
            go.append(f'\t\t"{dst}": true,\n')
        go.append("\t},\n")
    go.append("}\n\n")
    go.append("// funcReturns says what each function RETURNS, as a RULE rather\n")
    go.append("// than an answer: `fixed` is the same type whatever it was given,\n")
    go.append("// `args` is the coercion of its arguments, `promote` widens that\n")
    go.append("// (INT to BIGINT, FLOAT to DOUBLE) and `array` wraps it. Probed by\n")
    go.append("// running each class over an integer, a real and a string and\n")
    go.append("// reading the rule out of how the answers move -- recording the\n")
    go.append("// three ANSWERS would be a table right for those arguments and\n")
    go.append("// wrong for every other call.\n")
    go.append("type funcReturn struct {\n\tKind string\n\tType string\n}\n\n")
    go.append("var funcReturns = map[string]map[string]funcReturn{\n")
    for d in sorted(returns):
        go.append(f'\t"{d}": {{\n')
        for cls in sorted(returns[d]):
            rule = returns[d][cls]
            go.append(
                f'\t\t"{cls}": {{Kind: "{rule["kind"]}", Type: "{rule.get("type", "")}"}},\n'
            )
        go.append("\t},\n")
    go.append("}\n\n")
    go.append("// unannotatedClasses are the classes the reference's annotator has no\n")
    go.append("// entry for. It answers UNKNOWN for those -- an answer, not a gap --\n")
    go.append("// and so does this. Anonymous is excluded: the port reads some calls\n")
    go.append("// anonymously that the reference reads as nodes of their own, and an\n")
    go.append("// UNKNOWN there would hide a parse gap behind an annotator's answer.\n")
    go.append("var unannotatedClasses = map[string]map[string]bool{\n")
    for d, names in unannotated_classes(OURS).items():
        go.append(f'\t"{d}": {{\n')
        for name in names:
            go.append(f'\t\t"{name}": true,\n')
        go.append("\t},\n")
    go.append("}\n\n")
    go.append("// supportsNullType: where false, a NULL-typed node ends as UNKNOWN.\n")
    go.append("// Databricks is the exception, and spells the surviving type VOID.\n")
    go.append("var supportsNullType = map[string]bool{\n")
    for d, v in supports_null.items():
        go.append(f'\t"{d}": {str(v).lower()},\n')
    go.append("}\n")
    out_go = a.out.parent.parent / "sqlglot" / "annotate_gen.go"
    out_go.write_text("".join(go))
    subprocess.run(["gofmt", "-w", str(out_go)], check=True)
    print(f"  coercion table -> {out_go}")
    print(f"{len(cases)} scope-free annotator cases written to {a.out} ({skipped} need a column)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
