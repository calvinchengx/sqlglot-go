#!/usr/bin/env python3
"""Write the README's numbers from the files that measure them.

    gen_readme.py            # rewrite the tables
    gen_readme.py --check    # fail if the README disagrees with the measurements

The tables used to be hand-copied out of `testdata/**/coverage.json`, and
nothing compared the copy. That is the same defect the service corpus itself
had a few hours before this script was written: its generator went on reading a
file the statements had moved out of, reported 96 where there were 148, and the
README kept showing the old figures because they are only rewritten when
somebody remembers. A number with two homes drifts; the fix is to give it one.

FAILS CLOSED, which is the point. Every marker must be present, every source
file must parse, every dialect that appears in one measurement must appear in
the other, and every dialect must be placed in DIALECTS below. A new dialect
therefore stops this script rather than being silently dropped from a table
that still looks complete -- which is precisely how a measurement covers less
than it claims while staying green.
"""

from __future__ import annotations

import argparse
import difflib
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
README = ROOT / "README.md"

# Reading order, widest first, rather than alphabetical: the neutral dialect is
# the bulk of the corpus and the four the service uses follow it. Listed here
# so a dialect nobody placed is an error, not a silent omission.
DIALECTS = ["neutral", "tsql", "postgres", "duckdb", "databricks"]


class Stale(Exception):
    """The README disagrees with what was measured."""


def load(path: pathlib.Path) -> dict:
    if not path.exists():
        raise SystemExit(f"{path.relative_to(ROOT)} does not exist; run `make test` first")
    return json.loads(path.read_text())


def service_table(cov: dict) -> str:
    by_category = cov["by_category"]
    if not by_category:
        raise SystemExit("the service coverage has no categories; the corpus is not being read")
    rows = "\n".join(
        f"| `{name}` | {v['parsed']} | {v['total']} |" for name, v in sorted(by_category.items())
    )
    return "| Category | Parsed | Of |\n|---|---|---|\n" + rows


def reference_table(cov: dict, tokens: dict) -> str:
    trees, toks = cov["by_dialect"], tokens["by_dialect"]
    unplaced = sorted(set(trees) - set(DIALECTS))
    if unplaced:
        raise SystemExit(
            f"these dialects are measured but not placed in DIALECTS: {', '.join(unplaced)}.\n"
            "Add them there deliberately rather than letting the table omit them."
        )
    missing_tokens = sorted(set(trees) - set(toks))
    if missing_tokens:
        raise SystemExit(f"no token coverage for: {', '.join(missing_tokens)}")

    rows = []
    for dialect in DIALECTS:
        if dialect not in trees:
            continue
        t, k = trees[dialect], toks[dialect]
        rows.append(
            f"| {dialect} | {k['matched']}/{k['total']} | {t['matched']} | "
            f"{t['total']} | {t['unparsed']} | {t['mismatched']} |"
        )
    header = "| Dialect | Tokens | Trees matched | Of | Unparsed | Mismatched |\n|---|---|---|---|---|---|\n"
    return header + "\n".join(rows)


def replace(text: str, name: str, body: str) -> str:
    start, end = f"<!-- {name}:start -->", f"<!-- {name}:end -->"
    if start not in text or end not in text:
        raise SystemExit(f"README has no {start} / {end} pair; nothing to write into")
    head, rest = text.split(start, 1)
    _, tail = rest.split(end, 1)
    return f"{head}{start}\n{body}\n{end}{tail}"


def prose_claims(text: str, cov: dict) -> None:
    """The numbers stated in sentences, which no table covers.

    Checked rather than written: they sit inside prose that a generator has no
    business rewriting, but an unchecked number in a sentence drifts exactly
    like an unchecked number in a table.
    """
    reference = cov["reference"][:12]
    if f"`{reference}`" not in text:
        raise Stale(f"the README does not name the pinned reference {reference}")

    floor = re.search(
        r"const floor = (\d+)", (ROOT / "harness" / "generate_test.go").read_text()
    )
    if not floor:
        raise SystemExit("could not find `const floor` in harness/generate_test.go")
    # The README writes the number with thousands separators; the floor does not.
    claimed = re.search(r"\*\*([\d,]+) of the statements it parses are written back", text)
    if not claimed:
        raise Stale("the README no longer states how many statements are written back identically")
    if claimed.group(1).replace(",", "") != floor.group(1):
        raise Stale(
            f"the README says {claimed.group(1)} statements are written back identically, "
            f"generate_test.go's floor is {floor.group(1)}"
        )


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="fail instead of writing")
    args = ap.parse_args()

    cov = load(ROOT / "testdata" / "coverage.json")
    tokens = load(ROOT / "testdata" / "token_coverage.json")
    service = load(ROOT / "testdata" / "service" / "coverage.json")

    before = README.read_text()
    after = replace(before, "service", service_table(service))
    after = replace(after, "coverage", reference_table(cov, tokens))

    try:
        prose_claims(after, cov)
    except Stale as e:
        print(f"FAIL: {e}")
        return 1

    if args.check:
        if after != before:
            print("FAIL: the README's tables are not what was measured.\n")
            sys.stdout.writelines(
                difflib.unified_diff(
                    before.splitlines(keepends=True),
                    after.splitlines(keepends=True),
                    fromfile="README.md",
                    tofile="measured",
                )
            )
            print("\nRun `make readme`.")
            return 1
        print(f"the README's tables are the measurements ({cov['reference'][:12]})")
        return 0

    README.write_text(after)
    print(f"README tables written from testdata ({cov['reference'][:12]})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
