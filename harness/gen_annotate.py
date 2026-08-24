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
import sys


def load_pairs(sqlglot_dir: pathlib.Path):
    import importlib.util

    spec = importlib.util.spec_from_file_location("h", sqlglot_dir / "tests/helpers.py")
    helpers = importlib.util.module_from_spec(spec)
    sys.modules["h"] = helpers
    spec.loader.exec_module(helpers)
    return list(helpers.load_sql_fixture_pairs("optimizer/annotate_types.sql"))


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
        dialect = meta.get("dialect") or ""
        if dialect not in OURS:
            skipped += 1
            continue
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

    a.out.write_text(json.dumps({"cases": cases}, indent=1, sort_keys=True) + "\n")
    print(f"{len(cases)} scope-free annotator cases written to {a.out} ({skipped} need a column)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
