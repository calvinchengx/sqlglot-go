#!/usr/bin/env python3
"""Run the port's SQL through DuckDB and check it MEANS what it was given.

    make oracle-exec

Every other harness here compares parse trees or generated strings. Both are
fidelity checks: they prove the port agrees with sqlglot, never that either is
right. That is enough while the port only reads and writes -- a wrong tree
shows up as a different tree -- but it stops being enough the moment anything
REWRITES a tree. A rewrite that is wrong but plausible produces SQL that still
parses, still round-trips, and still runs. It just returns different rows.

So this is the other kind of check: take the statement as it was written, take
what the port writes back, run BOTH on DuckDB, and compare the results. It is
the only test in the repository whose failure means "these two queries ask
different questions" rather than "these two strings differ".

Why Python. Go cannot embed DuckDB without cgo, and CI proves the port on five
platforms -- including Windows on arm64 -- with a Go toolchain and nothing
else. Putting an engine behind cgo would end that. The Go side emits the pairs
(harness/execute_test.go) and the engine lives here, exactly as the reference
does.

The verdicts:

  agree            same rows, same order. What the port is held to.
  reordered        same rows, different order. Not a failure: without an
                   ORDER BY neither query promised an order.
  DIVERGED         different rows. The two queries ask different questions.
  nondeterministic the statement disagrees with ITSELF across two runs --
                   CURRENT_TIME, RANDOM. Excluded rather than denylisted by
                   keyword, so the harness discovers the class instead of
                   being told it.
  unrunnable       DuckDB will not run what it was GIVEN, so there is nothing
                   to compare the port against. Most of the corpus: it is a
                   transpiler's test suite, not a workload.
  port refused     the port would not write it. Counted by the generator
                   harness, not here.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent


def rows(con, sql: str):
    """The result as a list of tuples -- not its repr.

    An earlier cut compared repr strings and recovered 'same rows, different
    order' by splitting one on `), (`. That is not a parser: the leading and
    trailing parentheses land on different fragments, so two orderings of the
    SAME rows sorted differently and the harness reported a divergence that
    was not one. Compare the rows.
    """
    cur = con.execute(sql)
    try:
        return cur.fetchall()
    except Exception:
        return None


def multiset(result):
    """A stable key for 'the same rows in some order'."""
    return sorted(result, key=repr) if isinstance(result, list) else result


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--pairs", required=True, help="JSONL from TestEmitExecutionPairs")
    ap.add_argument("--gate", default=str(ROOT / "testdata" / "execution.json"))
    ap.add_argument("--update", action="store_true", help="rewrite the gate from this run")
    a = ap.parse_args()

    try:
        import duckdb
    except ModuleNotFoundError:
        print(
            "the execution oracle needs DuckDB:\n"
            "    pip install duckdb\n"
            "or run it without installing anything:\n"
            '    make oracle-exec PYTHON="uv run --with duckdb python"',
            file=sys.stderr,
        )
        return 2

    # Executing a transpiler's test corpus is not a read-only act. The corpus
    # contains ATTACH, COPY and CREATE, and the first run of this harness left
    # a file.db and a query.json in the repository root. So the engine runs
    # with its working directory inside a scratch dir that goes away with it:
    # what the SQL writes, it writes there.
    scratch = tempfile.TemporaryDirectory(prefix="sqlglot-go-oracle-")
    here = pathlib.Path.cwd()
    os.chdir(scratch.name)

    a.gate = str(pathlib.Path(a.gate).resolve())
    a.pairs = str(pathlib.Path(a.pairs).resolve())
    gate = json.loads(pathlib.Path(a.gate).read_text()) if pathlib.Path(a.gate).exists() else {}
    known = {d["sql"]: d for d in gate.get("known_divergences", [])}

    con = duckdb.connect()
    tally: dict[str, int] = {}
    diverged: list[dict[str, str]] = []

    def bump(k: str) -> None:
        tally[k] = tally.get(k, 0) + 1

    def compare(who: str, src: str, first: str, out: str) -> None:
        """Does `out` ask the same question as `src`?"""
        if src.strip() == out.strip():
            bump(who + ": identical, nothing to compare")
            return
        try:
            got = rows(con, out)
        except Exception as e:
            bump("DIVERGED")
            diverged.append({"sql": src, "who": who, "written": out,
                             "why": "does not run: " + str(e).split("\n")[0][:70]})
            return
        if got == first:
            bump(who + ": agree")
        elif multiset(got) == multiset(first):
            bump(who + ": same rows, different order")
        else:
            bump("DIVERGED")
            diverged.append({"sql": src, "who": who, "written": out,
                             "why": f"{repr(first)[:56]} != {repr(got)[:56]}"})

    for line in pathlib.Path(a.pairs).read_text().splitlines():
        rec = json.loads(line)
        src = rec["sql"]

        try:
            first = rows(con, src)
        except Exception:
            bump("the statement as written does not run")
            continue

        # A statement that disagrees with itself cannot arbitrate anything.
        try:
            if rows(con, src) != first:
                bump("nondeterministic")
                continue
        except Exception:
            bump("nondeterministic")
            continue

        # The REFERENCE's rendering is checked too, not just the port's. The
        # port reproduces sqlglot byte for byte on most of the corpus, so a
        # semantic bug in sqlglot's own round trip is one the port inherits
        # silently -- and no differential against sqlglot can ever see it.
        compare("reference", src, first, rec["reference"])
        if "port" in rec:
            compare("port", src, first, rec["port"])
        else:
            bump("port refused it")

    os.chdir(here)
    scratch.cleanup()

    for k in sorted(tally, key=lambda k: -tally[k]):
        print("%6d  %s" % (tally[k], k))

    compared = sum(n for k, n in tally.items() if k.endswith(": agree"))
    unexplained = [d for d in diverged if d["sql"] not in known]

    if a.update:
        pathlib.Path(a.gate).write_text(
            json.dumps(
                {
                    "comparable_floor": compared,
                    "known_divergences": sorted(
                        (known | {d["sql"]: d for d in diverged}).values(),
                        key=lambda d: d["sql"],
                    ),
                },
                indent=1,
            )
            + "\n"
        )
        print("\nwrote %s: floor %d, %d known divergence(s)" % (a.gate, compared, len(diverged)))
        return 0

    ok = True
    if unexplained:
        ok = False
        print("\n%d UNEXPLAINED divergence(s) -- two queries, different answers:" % len(unexplained))
        for d in unexplained:
            print("\n  in   : %s" % d["sql"][:96])
            print("  %-5s: %s" % (d["who"], d["written"][:96]))
            print("  %s" % d["why"])
        print(
            "\nEach is either a port bug or the reference's, and the string\n"
            "differential cannot tell you which. Investigate, then record it in\n"
            "%s with a note once it is understood." % a.gate
        )

    floor = gate.get("comparable_floor")
    if floor is not None and compared < floor:
        ok = False
        print("\nEXECUTION COVERAGE REGRESSED: %d compared, floor %d" % (compared, floor))

    if ok:
        print("\n%d statements executed and compared." % compared)
        if known:
            print(
                "%d known divergence(s), recorded in docs/upstream-issues.md and\n"
                "reproduced on purpose rather than worked around." % len(known)
            )
        else:
            print("None asks a different question from the statement it came from.")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
