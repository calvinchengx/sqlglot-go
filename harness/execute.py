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
FIXTURES = ROOT / "testdata" / "fixtures" / "schema.sql"


class Engine:
    """One database, and how to run a statement in it without keeping it.

    Isolation is per engine because the mechanism is: DuckDB gets a scratch
    working directory (its side effects are FILES -- ATTACH, COPY), PostgreSQL
    gets a transaction that is always rolled back (its side effects are
    CATALOG -- CREATE, DROP). Without either, a corpus of a transpiler's tests
    leaves debris and later statements fail on it: the first run of this
    harness left a file.db in the repository root, and the PostgreSQL run
    failed with 'relation "t" already exists'.
    """

    name = ""
    dialect = ""

    def rows(self, sql: str):
        raise NotImplementedError

    def load_fixtures(self) -> None:
        """The tables the corpus names but never creates.

        Same schema in every engine, so a statement cannot mean one thing here
        and another there. See testdata/fixtures/schema.sql for why the tables
        have rows rather than being empty.
        """
        raise NotImplementedError

    def close(self) -> None:
        pass


class DuckDB(Engine):
    name = "duckdb"
    dialect = "duckdb"

    def __init__(self):
        import duckdb

        self._scratch = tempfile.TemporaryDirectory(prefix="sqlglot-go-oracle-")
        self._home = pathlib.Path.cwd()
        os.chdir(self._scratch.name)
        self._con = duckdb.connect()

    def load_fixtures(self) -> None:
        self._con.execute(FIXTURES.read_text())

    def rows(self, sql: str):
        cur = self._con.execute(sql)
        try:
            return cur.fetchall()
        except Exception:
            return None

    def close(self) -> None:
        os.chdir(self._home)
        self._scratch.cleanup()


class Postgres(Engine):
    name = "postgres"
    dialect = "postgres"

    # A schema of its own, named for this harness. Everything the oracle
    # creates lives here and is dropped on the next run, so a server that
    # outlives one run does not fail the next with "relation t already
    # exists" -- and so pointing PGDSN at a database that holds anything else
    # can only ever destroy what this harness itself put there.
    SCHEMA = "sqlglot_go_oracle"

    def __init__(self, dsn: str):
        import psycopg

        # search_path is set on the CONNECTION rather than by a statement:
        # every statement below runs in a transaction that is rolled back,
        # which would undo a SET.
        self._con = psycopg.connect(dsn + f" options=-csearch_path={self.SCHEMA}")

    def load_fixtures(self) -> None:
        # Committed explicitly: every statement below is rolled back, and the
        # fixture must survive that or the first rollback would take it away.
        self._con.execute(f"DROP SCHEMA IF EXISTS {self.SCHEMA} CASCADE")
        self._con.execute(f"CREATE SCHEMA {self.SCHEMA}")
        self._con.execute(FIXTURES.read_text())
        self._con.commit()

    def rows(self, sql: str):
        # Every statement runs and is then UNDONE, so a CREATE in the corpus
        # cannot make the next statement fail. Anything that refuses to run
        # inside a transaction (VACUUM, CREATE DATABASE) raises here and is
        # counted as not running, which is the honest answer for this harness.
        with self._con.cursor() as cur:
            try:
                cur.execute(sql)
                try:
                    return cur.fetchall()
                except Exception:
                    return None
            finally:
                self._con.rollback()


def multiset(result):
    """A stable key for 'the same rows in some order'.

    An earlier cut compared repr strings and recovered this by splitting one
    on `), (`. That is not a parser: the outer parentheses land on different
    fragments, so two orderings of the SAME rows sorted differently and the
    harness reported a divergence that was not one. Compare the rows.
    """
    return sorted(result, key=repr) if isinstance(result, list) else result


def open_engines(dsn: str | None) -> tuple[list[Engine], list[str]]:
    """Whatever can be reached. Skipping is reported, never silent."""
    engines: list[Engine] = []
    skipped: list[str] = []
    try:
        e = DuckDB()
        e.load_fixtures()
        engines.append(e)
    except ModuleNotFoundError:
        skipped.append(
            "duckdb: not installed. `pip install duckdb`, or run\n"
            '          make oracle-exec PYTHON="uv run --with duckdb python"'
        )
    if dsn:
        try:
            e = Postgres(dsn)
            e.load_fixtures()
            engines.append(e)
        except ModuleNotFoundError:
            skipped.append("postgres: psycopg not installed (`pip install 'psycopg[binary]'`)")
        except Exception as e:
            skipped.append("postgres: cannot connect -- " + str(e).split("\n")[0][:70])
    else:
        skipped.append(
            "postgres: no server named. Set PGDSN, or start one:\n"
            "          docker run -d -p 5432:5432 -e POSTGRES_HOST_AUTH_METHOD=trust postgres:17-alpine"
        )
    return engines, skipped


def run(engine: Engine, pairs, known) -> tuple[dict[str, int], list[dict[str, str]]]:
    tally: dict[str, int] = {}
    diverged: list[dict[str, str]] = []

    def bump(k: str) -> None:
        tally[k] = tally.get(k, 0) + 1

    def compare(who: str, src: str, first, out: str) -> None:
        """Does `out` ask the same question as `src`?"""
        if src.strip() == out.strip():
            bump(who + ": identical, nothing to compare")
            return
        try:
            got = engine.rows(out)
        except Exception as e:
            bump("DIVERGED")
            diverged.append({"engine": engine.name, "sql": src, "who": who, "written": out,
                             "why": "does not run: " + str(e).split("\n")[0][:70]})
            return
        # The REWRITTEN side has to be stable too. Checking only the input was
        # not enough: `USING SAMPLE 10%` returned the same thing twice and a
        # different thing on the sixth run, so an unstable rewrite was
        # reported as a divergence. A comparison is only meaningful if BOTH
        # sides reproduce.
        try:
            if engine.rows(out) != got:
                bump(who + ": nondeterministic")
                return
        except Exception:
            bump(who + ": nondeterministic")
            return
        if got != first and multiset(got) != multiset(first):
            # About to accuse. Look once more at BOTH sides first: a statement
            # unstable enough to slip past the repeat checks above would
            # otherwise be reported as a divergence, and a gate that cries
            # wolf on a rerun is a gate people learn to ignore. Confirming
            # costs two executions on the rare path and nothing on the common
            # one.
            try:
                if engine.rows(src) != first or engine.rows(out) != got:
                    bump(who + ": nondeterministic")
                    return
            except Exception:
                bump(who + ": nondeterministic")
                return

        if got == first:
            # Two queries that both return NOTHING agree about nothing. With
            # fixture tables in place this is the cheap way to look busy --
            # any pair of mistakes over an empty result set matches -- so it
            # is counted apart and does NOT reach the comparable floor.
            if first == [] or first is None:
                bump(who + ": both returned no rows")
            else:
                bump(who + ": agree")
        elif multiset(got) == multiset(first):
            bump(who + ": same rows, different order")
        else:
            bump("DIVERGED")
            diverged.append({"engine": engine.name, "sql": src, "who": who, "written": out,
                             "why": f"{repr(first)[:52]} != {repr(got)[:52]}"})

    for rec in pairs:
        if rec.get("dialect") != engine.dialect:
            continue
        src = rec["sql"]
        try:
            first = engine.rows(src)
        except Exception:
            bump("the statement as written does not run")
            continue
        # A statement that disagrees with itself cannot arbitrate anything.
        # Three looks rather than two: sampling and hashing can repeat once by
        # luck, and every extra look is cheap next to a false divergence.
        try:
            if any(engine.rows(src) != first for _ in range(2)):
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
        # The port's own REWRITE, which is the only thing here that no string
        # or tree differential can vouch for.
        if "simplified" in rec:
            compare("simplified", src, first, rec["simplified"])
    return tally, diverged


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--pairs", required=True, help="JSONL from TestEmitExecutionPairs")
    ap.add_argument("--gate", default=str(ROOT / "testdata" / "execution.json"))
    ap.add_argument("--postgres", default=os.environ.get("PGDSN"), help="libpq DSN, or $PGDSN")
    ap.add_argument("--update", action="store_true", help="rewrite the gate from this run")
    a = ap.parse_args()

    gate_path = pathlib.Path(a.gate).resolve()
    pairs = [json.loads(x) for x in pathlib.Path(a.pairs).resolve().read_text().splitlines()]
    gate = json.loads(gate_path.read_text()) if gate_path.exists() else {}
    known = {(d.get("engine", "duckdb"), d["sql"]) for d in gate.get("known_divergences", [])}
    floors = gate.get("comparable_floor", {})

    engines, skipped = open_engines(a.postgres)
    if not engines:
        for note in skipped:
            print("  " + note, file=sys.stderr)
        return 2

    results: dict[str, dict] = {}
    all_diverged: list[dict[str, str]] = []
    for engine in engines:
        tally, diverged = run(engine, pairs, known)
        engine.close()
        compared = sum(n for k, n in tally.items() if k.endswith(": agree"))
        results[engine.name] = {"tally": tally, "compared": compared}
        all_diverged.extend(diverged)

    for name, r in results.items():
        print("== %s" % name)
        for k in sorted(r["tally"], key=lambda k: -r["tally"][k]):
            print("  %6d  %s" % (r["tally"][k], k))
        print()
    for note in skipped:
        print("  skipped %s" % note)
    if skipped:
        print()

    unexplained = [d for d in all_diverged if (d["engine"], d["sql"]) not in known]

    if a.update:
        seen = {(d.get("engine", "duckdb"), d["sql"]): d for d in gate.get("known_divergences", [])}
        seen |= {(d["engine"], d["sql"]): d for d in all_diverged}
        gate_path.write_text(
            json.dumps(
                {
                    "_comment": gate.get("_comment", ""),
                    "comparable_floor": floors | {n: r["compared"] for n, r in results.items()},
                    "known_divergences": sorted(seen.values(), key=lambda d: (d.get("engine",""), d["sql"])),
                },
                indent=1,
            )
            + "\n"
        )
        print("wrote %s" % gate_path)
        return 0

    ok = True
    if unexplained:
        ok = False
        print("%d UNEXPLAINED divergence(s) -- two queries, different answers:" % len(unexplained))
        for d in unexplained:
            print("\n  [%s] in : %s" % (d["engine"], d["sql"][:88]))
            print("  %11s: %s" % (d["who"], d["written"][:88]))
            print("  %s" % d["why"])
        print(
            "\nEach is either a port bug or the reference's, and the string\n"
            "differential cannot tell you which. Investigate, then record it in\n"
            "%s with a note once it is understood.\n" % gate_path
        )

    for name, r in results.items():
        floor = floors.get(name)
        if floor is not None and r["compared"] < floor:
            ok = False
            print("EXECUTION COVERAGE REGRESSED for %s: %d compared, floor %d"
                  % (name, r["compared"], floor))

    if ok:
        total = sum(r["compared"] for r in results.values())
        print("%d statements executed and compared across %d engine(s)."
              % (total, len(results)))
        if known:
            print(
                "%d known divergence(s), recorded in docs/upstream-issues.md and\n"
                "reproduced on purpose rather than worked around." % len(known)
            )
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
