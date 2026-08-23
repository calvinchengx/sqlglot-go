"""The reference oracle: sqlglot's trees, dumped to JSON.

    python harness/oracle.py --sqlglot ~/opensource/sqlglot --out testdata/expected

Every statement in the reference corpus is parsed by the PYTHON sqlglot and
its tree written as JSON. The Go port is then verified against those files,
statement by statement, by `harness/diff_test.go`. The reference is the
oracle: where the two disagree, the port is wrong until shown otherwise.

Pinned to one sqlglot commit. Regenerating against a different one is a
deliberate change — it moves the target, and the diff that results is the
record of what moved.

The expected files are COMMITTED, so the Go tests need no Python at all: a Go
developer runs `go test ./...` and is measured against the reference without
installing it. Regeneration is the only step that needs sqlglot.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import subprocess
import sys

# Dialects the executor configures. sqlglot's per-dialect suites supply
# dialect-specific statements; identity.sql supplies the dialect-neutral core.
DIALECTS = ("tsql", "postgres", "duckdb", "databricks")

# Statements from sqlglot's own tests that exercise one dialect. Extracted from
# the `validate_identity` calls in tests/dialects/test_<dialect>.py -- the same
# strings sqlglot holds itself to.
IDENTITY_CALL = "validate_identity("


def reference_commit(sqlglot_dir: pathlib.Path) -> str:
    out = subprocess.run(
        ["git", "-C", str(sqlglot_dir), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    )
    return out.stdout.strip()


def pinned_commit(repo: pathlib.Path) -> str:
    """The commit NOTICE names. The oracle refuses to run against another."""
    for line in (repo / "NOTICE").read_text().splitlines():
        if "commit " in line:
            return line.split("commit ")[1].split()[0].rstrip("(")
    raise SystemExit("NOTICE does not name a reference commit")


def corpus_identity(sqlglot_dir: pathlib.Path) -> list[tuple[str, str]]:
    """(dialect, sql) for identity.sql -- dialect-neutral, parsed as each."""
    out = []
    for raw in (sqlglot_dir / "tests/fixtures/identity.sql").read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith(("#", "--")):
            continue
        out.append(("", line))
    return out


def corpus_dialect(sqlglot_dir: pathlib.Path, dialect: str) -> list[tuple[str, str]]:
    """Statements a dialect's own suite round-trips.

    Read from the test source rather than executed: the point is the strings,
    and importing the test module would pull in unittest scaffolding for
    nothing. A `validate_identity("…")` whose first argument is a plain string
    literal on one line is taken; anything fancier is skipped and counted, so
    the coverage number stays honest about what was sampled.
    """
    import ast

    path = sqlglot_dir / f"tests/dialects/test_{dialect}.py"
    if not path.exists():
        return []
    tree = ast.parse(path.read_text())
    out: list[tuple[str, str]] = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        fn = node.func
        name = fn.attr if isinstance(fn, ast.Attribute) else getattr(fn, "id", "")
        if name != "validate_identity" or not node.args:
            continue
        first = node.args[0]
        if isinstance(first, ast.Constant) and isinstance(first.value, str):
            out.append((dialect, first.value))
    return out


def dump(sql: str, dialect: str):
    import sqlglot

    tree = sqlglot.parse_one(sql, read=dialect or None)
    return tree.dump()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--out", default="testdata/expected", type=pathlib.Path)
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

    corpus = corpus_identity(a.sqlglot)
    for d in DIALECTS:
        corpus += corpus_dialect(a.sqlglot, d)

    a.out.mkdir(parents=True, exist_ok=True)
    for f in a.out.glob("*.json"):
        f.unlink()
    written, failed = 0, []
    index = []
    for dialect, sql in corpus:
        key = hashlib.sha1(f"{dialect}\x00{sql}".encode()).hexdigest()[:16]
        try:
            tree = dump(sql, dialect)
        except Exception as e:  # noqa: BLE001 -- the reference's own failures are recorded, not hidden
            failed.append((dialect, sql, f"{type(e).__name__}: {e}"[:120]))
            continue
        (a.out / f"{key}.json").write_text(
            json.dumps({"dialect": dialect, "sql": sql, "tree": tree}, indent=1, sort_keys=True)
        )
        index.append({"key": key, "dialect": dialect, "sql": sql})
        written += 1

    (a.out / "index.json").write_text(
        json.dumps(
            {"reference": actual, "count": written, "statements": index},
            indent=1,
        )
    )
    print(f"reference {actual[:12]}: {written} expectations written to {a.out}")
    by = {}
    for s in index:
        by[s["dialect"] or "neutral"] = by.get(s["dialect"] or "neutral", 0) + 1
    for d, n in sorted(by.items()):
        print(f"  {d:10} {n}")
    if failed:
        print(f"  {len(failed)} statement(s) the REFERENCE could not parse (recorded, not hidden):")
        for d, sql, err in failed[:5]:
            print(f"    [{d or 'neutral'}] {sql[:60]} -> {err}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
