#!/usr/bin/env python3
"""Ingest the reference's `anonymize` + `render` over the port's own corpus.

    python harness/gen_anonymize.py --sqlglot ~/opensource/sqlglot

`sqlglot.anonymize` works on TOKENS and raw SQL text, not a parsed tree, so
it needs no fixture of its own -- the statements already harvested into
`testdata/expected` (the reference's own identity.sql and per-dialect
fixtures, tokenizable whether or not the port can parse them) are the
corpus, and what `anonymize` then `render` produce over each is the
expectation. Deterministic: the counter that hands out aliases starts fresh
every call, so re-running this against the same commit reproduces the same
file byte for byte.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys


def reference_commit(sqlglot_dir: pathlib.Path) -> str:
    out = subprocess.run(
        ["git", "-C", str(sqlglot_dir), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    )
    return out.stdout.strip()


def pinned_commit(repo: pathlib.Path) -> str:
    for line in (repo / "NOTICE").read_text().splitlines():
        if "commit " in line:
            return line.split("commit ")[1].split()[0].rstrip("(")
    raise SystemExit("NOTICE does not name a reference commit")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--expected", default="testdata/expected", type=pathlib.Path)
    ap.add_argument("--out", default="testdata/anonymize.json", type=pathlib.Path)
    a = ap.parse_args()

    repo = pathlib.Path(__file__).resolve().parent.parent
    sys.path.insert(0, str(a.sqlglot))
    actual, pinned = reference_commit(a.sqlglot), pinned_commit(repo)
    if actual != pinned:
        raise SystemExit(
            f"reference checkout is at {actual[:12]} but NOTICE pins {pinned[:12]}.\n"
            "Either check out the pinned commit, or update NOTICE deliberately and "
            "commit the regenerated expectations with it."
        )

    from sqlglot.anonymize import anonymize, render

    index = json.loads((a.expected / "index.json").read_text())

    cases = []
    for stmt in index["statements"]:
        dialect, sql = stmt["dialect"], stmt["sql"]
        tokens = anonymize(sql, dialect or None)
        rendered = render(sql, tokens, dialect or None)
        cases.append({"dialect": dialect, "sql": sql, "rendered": rendered})

    a.out.write_text(
        json.dumps({"reference": actual, "cases": cases}, indent=1, sort_keys=True) + "\n"
    )
    print(f"reference {actual[:12]}: {len(cases)} statements anonymized")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
