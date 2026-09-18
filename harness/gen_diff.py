#!/usr/bin/env python3
"""Ingest the reference's `diff` over the port's own simplify pairs.

    python harness/gen_diff.py --sqlglot ~/opensource/sqlglot

`sqlglot.diff` has no fixture of its own -- its own test suite is entirely
hand-written pairs. `testdata/simplify.json`'s 480 (sql, expected) pairs are
already a corpus of two real, related trees apiece, so they stand in for one:
each pair's SQL and its simplified form become diff's source and target.

Recorded DELTA-ONLY (Insert/Remove/Move/Update, no Keep) and SORTED by
(kind, dumped JSON) rather than in the order the reference emits them: the
reference's own edit script for Remove/Insert entries iterates a Python SET
keyed by object id, an ordering this port owes nothing to reproducing. What
the contract actually is -- which edits exist -- survives the sort; how
Python happened to iterate a set of memory addresses does not.
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
    ap.add_argument("--simplify", default="testdata/simplify.json", type=pathlib.Path)
    ap.add_argument("--out", default="testdata/diff.json", type=pathlib.Path)
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

    import sqlglot
    from sqlglot.diff import Insert, Move, Remove, Update, diff

    pairs = json.loads(a.simplify.read_text())["pairs"]

    cases = []
    skipped = 0
    for pair in pairs:
        dialect, sql, expected = pair["dialect"], pair["sql"], pair["expected"]
        try:
            source = sqlglot.parse_one(sql, read=dialect or None)
            target = sqlglot.parse_one(expected, read=dialect or None)
        except Exception:  # noqa: BLE001 -- the reference cannot read it either
            skipped += 1
            continue

        edits = diff(source, target, delta_only=True)
        recorded = []
        for e in edits:
            if isinstance(e, (Insert, Remove)):
                recorded.append({"kind": type(e).__name__, "expression": e.expression.dump()})
            else:
                assert isinstance(e, (Move, Update))
                recorded.append({
                    "kind": type(e).__name__,
                    "source": e.source.dump(),
                    "target": e.target.dump(),
                })
        recorded.sort(key=lambda r: (r["kind"], json.dumps(r, sort_keys=True)))

        cases.append({
            "dialect": dialect, "source_sql": sql, "target_sql": expected, "edits": recorded,
        })

    a.out.write_text(
        json.dumps({"reference": actual, "cases": cases}, indent=1, sort_keys=True) + "\n"
    )
    print(f"reference {actual[:12]}: {len(cases)} pairs diffed, {skipped} skipped (unreadable)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
