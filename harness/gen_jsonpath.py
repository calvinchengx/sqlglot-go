#!/usr/bin/env python3
"""Ingest the reference's JSONPath conformance suite.

    python harness/gen_jsonpath.py --sqlglot ~/opensource/sqlglot

`tests/fixtures/jsonpath/cts.json` is the JSONPath Compliance Test Suite the
reference's own `tests/test_jsonpath.py` holds `sqlglot.jsonpath.parse`
against: 526 cases, each a selector string and either the tree it must parse
into or a mark that it must be refused. The reference is the oracle here, not
the standard the CTS itself is drawn from -- sqlglot's parser is knowingly
more lenient in places (a leading `?` in a filter is optional, for one), so a
case the CTS calls invalid can still be one the reference reads.

Recorded as a TREE (`dump()`), not the `.sql()` string test_jsonpath.py
compares against: this port has no standalone JSONPath-to-string writer of
its own -- `writeJSONPath` only knows how to embed a path inside the handful
of dialect-specific call shapes JSON_EXTRACT and friends use -- and the tree
is what every other expectation in this repo is already held to.
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
    ap.add_argument("--out", default="testdata/jsonpath.json", type=pathlib.Path)
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

    from sqlglot.errors import ParseError, TokenError
    from sqlglot.jsonpath import parse

    raw = json.loads((a.sqlglot / "tests/fixtures/jsonpath/cts.json").read_text())

    cases = []
    for t in raw["tests"]:
        selector = t["selector"]
        if t.get("invalid_selector"):
            try:
                parse(selector)
            except (ParseError, TokenError):
                cases.append({"name": t["name"], "selector": selector, "valid": False})
                continue
            # The reference accepts a selector the CTS calls invalid -- its
            # own parser is more lenient than the standard in places, and
            # that leniency is exactly what the port has to match. Recorded
            # as a normal valid case: what the reference actually built.
        try:
            tree = parse(selector).dump()
        except (ParseError, TokenError):
            cases.append({"name": t["name"], "selector": selector, "valid": False})
            continue
        cases.append({"name": t["name"], "selector": selector, "valid": True, "tree": tree})

    a.out.write_text(
        json.dumps({"reference": actual, "cases": cases}, indent=1, sort_keys=True) + "\n"
    )
    valid = sum(1 for c in cases if c["valid"])
    print(f"reference {actual[:12]}: {len(cases)} CTS cases, {valid} the reference reads")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
