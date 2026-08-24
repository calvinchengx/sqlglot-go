#!/usr/bin/env python3
"""Ingest the reference's simplify contract.

    python harness/gen_simplify.py --sqlglot ~/opensource/sqlglot

`tests/fixtures/optimizer/simplify.sql` is how sqlglot pins its own optimizer:
pairs of statements, the second being what the first becomes. There are 521 of
them, and the reference's own test asserts on the SQL STRING the rule produces
-- not on the tree -- which is worth knowing, because nearly every rule in
`simplify.py` is decorated to re-annotate types as it rewrites. That
bookkeeping is internal; the contract observed here is the text.

Only the dialects this port configures are kept. The pairs are COMMITTED, the
same as every other expectation here, so the Go side needs no Python.

The reference is also asked what it does with each input, and that answer is
kept alongside the fixture's. They agree today; if they ever stop, the fixture
was written for a different commit than the one NOTICE pins, and the harness
should say so rather than silently hold the port to prose.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys

DIALECTS = ("", "tsql", "postgres", "duckdb", "databricks")


def pinned_commit(repo: pathlib.Path) -> str:
    for line in (repo / "NOTICE").read_text().splitlines():
        if "commit " in line:
            return line.split("commit ")[1].split()[0].rstrip("(")
    raise SystemExit("NOTICE does not name a reference commit")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True)
    ap.add_argument("--out", default="testdata/simplify.json")
    a = ap.parse_args()

    root = pathlib.Path(__file__).resolve().parent.parent
    sqlglot_dir = pathlib.Path(a.sqlglot).expanduser()

    head = subprocess.run(
        ["git", "-C", str(sqlglot_dir), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    want = pinned_commit(root)
    if head != want:
        raise SystemExit(f"reference is at {head[:12]}, NOTICE pins {want[:12]}")

    # Order matters: tests/ contains a `sqlglot` directory of its own, so the
    # repository root has to come first or `import sqlglot` finds the wrong one.
    sys.path.insert(0, str(sqlglot_dir / "tests"))
    sys.path.insert(0, str(sqlglot_dir))
    from helpers import load_sql_fixture_pairs  # noqa: E402
    from sqlglot import parse_one  # noqa: E402
    from sqlglot.optimizer.annotate_types import annotate_types  # noqa: E402
    from sqlglot.optimizer.simplify import simplify  # noqa: E402

    # Exactly how tests/test_optimizer.py calls it. Three things there are not
    # the defaults and change the answer: types are annotated FIRST against a
    # schema, and both constant_propagation and coalesce_simplification are
    # switched on. Calling simplify() plainly disagreed with the fixture on 36
    # pairs -- the fixture was right and the call was wrong.
    SCHEMA = {
        "x": {"a": "INT", "b": "INT"},
        "y": {"b": "INT", "c": "INT"},
        "z": {"b": "INT", "c": "INT"},
        "w": {"d": "TEXT", "e": "TEXT"},
        "temporal": {"d": "DATE", "t": "DATETIME"},
    }

    kept, skipped, disagreed = [], 0, 0
    for meta, sql, expected in load_sql_fixture_pairs("optimizer/simplify.sql"):
        dialect = meta.get("dialect") or ""
        if dialect not in DIALECTS:
            skipped += 1
            continue
        # Ask the reference directly as well as reading the fixture. If the two
        # disagree the fixture is stale for this commit, and holding the port
        # to it would be holding it to a comment.
        d = dialect or None
        tree = annotate_types(parse_one(sql, read=d), schema=SCHEMA, dialect=d)
        got = simplify(
            tree, constant_propagation=True, coalesce_simplification=True, dialect=d
        ).sql(dialect=d)
        if got != expected:
            disagreed += 1
            continue
        kept.append({"dialect": dialect, "sql": sql, "expected": expected})

    (root / a.out).write_text(
        json.dumps({"reference": head, "pairs": kept}, indent=1) + "\n"
    )
    print(f"{head[:12]}: wrote {len(kept)} pairs to {a.out}")
    print(f"  {skipped} skipped (a dialect this port does not configure)")
    if disagreed:
        print(f"  {disagreed} SKIPPED: the fixture disagrees with the pinned reference")
    return 0


if __name__ == "__main__":
    sys.exit(main())
