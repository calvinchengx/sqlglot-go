#!/usr/bin/env python3
"""Decide whether a fuzz finding is this port's bug or the reference's behaviour.

    python harness/adjudicate.py --sqlglot ~/opensource/sqlglot --candidates <file>

A fuzzer cannot call the oracle. At a hundred thousand executions a second, a
Python round trip per input is four orders of magnitude too slow, so the fuzz
targets in `sqlglot/fuzz_test.go` can only assert properties that hold
INDEPENDENTLY of sqlglot -- and that is a real limit, not a detail. Three
separate findings turned out to be the reference doing exactly the same thing:

    SELECT 1 JOIN a      the joined table moves into the select list
    SELECTa0000(0)A00    the call name is uppercased, so the tree differs
    +Do                  the unary plus is dropped, and a bare `Do` is a
                         Command rather than a column

Each cost a manual investigation -- parse it in Python, write it back, look.
Five times in one afternoon. This is that loop, in a batch: collect candidates
cheaply in Go, adjudicate them here against the reference, and let the answer
be mechanical.

The verdicts:

  YOURS     the reference round-trips this cleanly, so the port's failure is
            the port's own. Worth promoting into testdata/expected, where the
            differential will hold the port to it from then on.
  THEIRS    sqlglot does the same thing. Not a bug; the property asserted was
            stronger than the oracle's own guarantee.
  REFUSED   the reference cannot parse it either. Nothing to compare.

PRECONDITION, and it is not optional: every line in the candidates file must be
an input the PORT ALREADY FAILED ON. This tool examines the reference alone --
it has no opinion about the port and cannot form one -- so feeding it healthy
statements marks every one of them YOURS. The fuzz target writes the file for
exactly this reason; hand-written input is for reproducing a single case.

Input is one candidate per line, `dialect<TAB>sql`, which is what the fuzz
target writes when DAS_FUZZ_COLLECT names a file.
"""

from __future__ import annotations

import argparse
import collections
import json
import pathlib
import sys

PORT, REFERENCE, REFUSED = "YOURS", "THEIRS", "REFUSED"


QUOTED = None


def signature(cause: str) -> str:
    """The port's error with the variable part removed, so it groups.

    `unsupported statement: expression at "x"` and the same at `"y"` are one
    bug, and a fuzzer will find a thousand spellings of the token.
    """
    import re

    global QUOTED
    if QUOTED is None:
        QUOTED = re.compile(r'"(?:[^"\\]|\\.)*"')
    return QUOTED.sub("...", cause).strip()


def reference_round_trip(sql: str, dialect: str) -> tuple[str, str]:
    """How the REFERENCE behaves on this statement, and why.

    Returns a verdict for the reference alone: whether it parses, writes, and
    reads its own output back into the same tree. The port's behaviour is the
    caller's business -- this answers only "is the property being asserted one
    that sqlglot itself has?"
    """
    import sqlglot

    try:
        first = sqlglot.parse_one(sql, read=dialect or None)
    except Exception as e:
        return REFUSED, f"reference cannot parse: {type(e).__name__}"
    try:
        written = first.sql(dialect=dialect or None)
    except Exception as e:
        return REFERENCE, f"reference cannot write it either: {type(e).__name__}"
    try:
        second = sqlglot.parse_one(written, read=dialect or None)
    except Exception as e:
        return REFERENCE, f"reference cannot read back its own {written!r}: {type(e).__name__}"
    if repr(first) != repr(second):
        return REFERENCE, f"reference round-trips {sql!r} -> {written!r} into a different tree"
    return PORT, f"reference is stable: {sql!r} -> {written!r}"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--candidates", required=True, type=pathlib.Path)
    ap.add_argument(
        "--promote",
        type=pathlib.Path,
        help="write PORT verdicts here as one JSON line each, ready for the corpus",
    )
    a = ap.parse_args()

    sys.path.insert(0, str(a.sqlglot.expanduser()))

    if not a.candidates.exists():
        raise SystemExit(f"{a.candidates} does not exist; run the collector first")

    seen: set[tuple[str, str]] = set()
    verdicts: list[tuple[str, str, str, str]] = []
    # split("\n"), not splitlines(): Python also breaks on \v, \f, \x1c-\x1e,
    # \x85 and \u2028, all of which a fuzzer produces inside a statement and
    # none of which end a line in this file. Using splitlines() tore
    # candidates in half and then rejected the halves as malformed.
    for line in a.candidates.read_text().split("\n"):
        if not line.strip():
            continue
        parts = line.split("\t", 2)
        if len(parts) != 3:
            raise SystemExit(
                f"malformed candidate line (want dialect<TAB>error<TAB>sql): {line!r}"
            )
        dialect, cause, sql = parts
        if (dialect, sql) in seen:
            continue
        seen.add((dialect, sql))
        verdict, why = reference_round_trip(sql, dialect)
        verdicts.append((verdict, dialect, sql, why, signature(cause)))

    counts = collections.Counter(v for v, _, _, _, _ in verdicts)
    print(
        f"adjudicating {len(verdicts)} candidate(s) that the PORT failed on.\n"
        "A verdict of YOURS means the reference handles it cleanly, so the "
        "failure is the port's.\n"
    )
    # Grouped by the port's own error, because a session produces hundreds of
    # variations of a handful of causes and a flat list buries that. One
    # exemplar each, shortest first: the smallest reproducer is the one worth
    # opening.
    mine = [v for v in verdicts if v[0] == PORT]
    clusters: dict[str, list] = collections.defaultdict(list)
    for v in mine:
        clusters[v[4]].append(v)
    if clusters:
        print(f"{len(mine)} finding(s) that are the port's, in "
              f"{len(clusters)} distinct cause(s):\n")
    for sig, group in sorted(clusters.items(), key=lambda kv: -len(kv[1])):
        _, dialect, sql, why, _ = min(group, key=lambda v: len(v[2]))
        print(f"  {len(group):>5}x  {sig}")
        print(f"         smallest: [{dialect}] {sql!r}")
        print(f"         {why}\n")
    theirs = collections.Counter(v[4] for v in verdicts if v[0] == REFERENCE)
    if theirs:
        print("the reference does the same on:")
        for sig, n in theirs.most_common():
            print(f"  {n:>5}x  {sig}")
        print()
    print(
        f"\n{len(verdicts)} candidate(s): "
        + ", ".join(f"{n} {v}" for v, n in sorted(counts.items()))
    )

    if a.promote:
        port_bugs = [
            {"dialect": d, "sql": s, "cause": sig}
            for verdict, d, s, _, sig in verdicts
            if verdict == PORT
        ]
        a.promote.write_text("".join(json.dumps(c) + "\n" for c in port_bugs))
        print(f"wrote {len(port_bugs)} PORT verdict(s) to {a.promote}")

    # A YOURS verdict is a bug this port has and the reference does not, so
    # the exit code says so: this is meant to be runnable from a script.
    return 1 if counts[PORT] else 0


if __name__ == "__main__":
    raise SystemExit(main())
