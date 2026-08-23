"""Extract the SQL that data agent service actually runs, as a second corpus.

    python harness/gen_service_corpus.py \
        --service ~/calvinchengx/emulators/data-agent-service \
        --sqlglot ~/opensource/sqlglot

sqlglot's own fixtures measure the port against sqlglot. They are the right
oracle for correctness and the wrong yardstick for "can the executor switch
over": most of what is left unparsed there is dialect exotica -- XML output
clauses, `$tag$` heredocs, slice syntax -- that a data agent will never emit.

This corpus is the other measurement. It is the SQL this service is actually
held to: the gold answers in its evaluation suites, the statements its guard
contract permits, and the ones its adversarial corpus requires it to refuse.
When THIS reaches 100%, the Go guard can be rewritten over the tree.

Nothing here is authored. Every statement is read out of data agent service,
with the file it came from recorded, so the corpus cannot drift from what the
service is really tested against without the regeneration showing it.

Three categories, because "refused" is not one outcome:

  must_parse
      Statements the guard permits. A parse failure here is a usability wall:
      a question the agent answers today would start being refused.

  must_parse_to_refuse
      Statements the guard refuses for a REASON that depends on the tree --
      "table function", "not queryable", "read-only". If the parser cannot
      read them, they are still refused, but for the wrong reason, and the
      conformance suite checks the reason. This category is why the port must
      recognise DDL and DML far enough to name them, even though it will never
      execute either.

  may_refuse_unparsed
      Statements whose refusal reason IS that they do not parse. Here a parse
      failure is the correct answer, not a gap.
"""

from __future__ import annotations

import argparse
import ast
import json
import pathlib
import re
import subprocess
import sys

MUST_PARSE = "must_parse"
MUST_PARSE_TO_REFUSE = "must_parse_to_refuse"
MAY_REFUSE_UNPARSED = "may_refuse_unparsed"

# A refusal reason that means "this is not SQL" -- the only case where the
# parser failing is the right answer rather than a gap.
UNPARSEABLE_REASONS = {"parse", "empty"}


def dialects_by_source(service: pathlib.Path) -> dict[str, str]:
    """Map a configured source name to its dialect, from .env's DAS_SOURCES."""
    env = service / ".env"
    if not env.exists():
        return {}
    for line in env.read_text().splitlines():
        if not line.startswith("DAS_SOURCES="):
            continue
        try:
            sources = json.loads(line.split("=", 1)[1])
        except json.JSONDecodeError:
            return {}
        return {s["name"]: s.get("dialect", "tsql") for s in sources if "name" in s}
    return {}


def strings_in(node: ast.AST) -> list[str]:
    """Every plain string literal directly inside a list or tuple display."""
    out = []
    for element in getattr(node, "elts", []):
        if isinstance(element, ast.Constant) and isinstance(element.value, str):
            out.append(element.value)
        elif isinstance(element, ast.Tuple) and element.elts:
            first = element.elts[0]
            if isinstance(first, ast.Constant) and isinstance(first.value, str):
                out.append(first.value)
    return out


def pairs_in(node: ast.AST) -> list[tuple[str, str]]:
    """(sql, reason) from a list of 2-tuples, or from a dict display."""
    out: list[tuple[str, str]] = []
    if isinstance(node, ast.Dict):
        for key, value in zip(node.keys, node.values):
            if (
                isinstance(key, ast.Constant)
                and isinstance(key.value, str)
                and isinstance(value, ast.Constant)
                and isinstance(value.value, str)
            ):
                out.append((key.value, value.value))
        return out
    for element in getattr(node, "elts", []):
        if isinstance(element, ast.Tuple) and len(element.elts) == 2:
            sql, reason = element.elts
            if (
                isinstance(sql, ast.Constant)
                and isinstance(sql.value, str)
                and isinstance(reason, ast.Constant)
                and isinstance(reason.value, str)
            ):
                out.append((sql.value, reason.value))
    return out


def categorise(reason: str) -> str:
    return MAY_REFUSE_UNPARSED if reason.strip().lower() in UNPARSEABLE_REASONS else MUST_PARSE_TO_REFUSE


def from_python_guard_tests(path: pathlib.Path, default_dialect: str) -> list[dict]:
    """Read a guard test module without importing it.

    The lists are read out of the source rather than executed: importing would
    pull in pytest and the service's own package layout for nothing, and the
    point is the strings.
    """
    if not path.exists():
        return []
    tree = ast.parse(path.read_text())
    out: list[dict] = []
    rel = path.name

    def dialect_for(name: str, fallback: str) -> str:
        return "duckdb" if "DUCKDB" in name.upper() else fallback

    for node in tree.body:
        # Module-level ALLOWED / DENIED style lists.
        if isinstance(node, ast.Assign):
            for target in node.targets:
                if not isinstance(target, ast.Name):
                    continue
                name = target.id
                if "ALLOWED" in name:
                    for sql in strings_in(node.value):
                        out.append(
                            {
                                "sql": sql,
                                "dialect": dialect_for(name, default_dialect),
                                "category": MUST_PARSE,
                                "from": f"{rel}:{name}",
                            }
                        )
                elif "DENIED" in name or "REFUSED" in name:
                    for sql, reason in pairs_in(node.value):
                        out.append(
                            {
                                "sql": sql,
                                "dialect": dialect_for(name, default_dialect),
                                "category": categorise(reason),
                                "reason": reason,
                                "from": f"{rel}:{name}",
                            }
                        )

        # Parametrised tests: the adversarial corpus lives here.
        if isinstance(node, ast.FunctionDef):
            body = ast.dump(node)
            # The policy the test uses tells us the dialect: D is DuckDB.
            uses_duckdb = "id='D'" in body or 'id="D"' in body
            allowed = "allowed" in node.name or "honoured" in node.name
            for decorator in node.decorator_list:
                if not isinstance(decorator, ast.Call) or not decorator.args:
                    continue
                target = getattr(decorator.func, "attr", "")
                if target != "parametrize":
                    continue
                for arg in decorator.args[1:]:
                    for sql in strings_in(arg):
                        out.append(
                            {
                                "sql": sql,
                                "dialect": "duckdb" if uses_duckdb else default_dialect,
                                "category": MUST_PARSE if allowed else MUST_PARSE_TO_REFUSE,
                                "from": f"{rel}:{node.name}",
                            }
                        )
    return out


# Only the []string literal, not the assertions after it: the function body
# also contains format strings, and a "%q reported no table" is not SQL.
GO_ALLOWED = re.compile(
    r"func TestAllowed\(t \*testing\.T\) \{.*?\[\]string\{(.*?)\n\t\} \{", re.S
)
GO_STRING = re.compile(r'"((?:[^"\\]|\\.)*)"')


def from_go_guard_tests(path: pathlib.Path, dialect: str) -> list[dict]:
    if not path.exists():
        return []
    body = GO_ALLOWED.search(path.read_text())
    if not body:
        return []
    out = []
    for raw in GO_STRING.findall(body.group(1)):
        sql = raw.encode().decode("unicode_escape")
        if sql.strip():
            out.append(
                {"sql": sql, "dialect": dialect, "category": MUST_PARSE, "from": f"{path.name}:TestAllowed"}
            )
    return out


def from_evals(service: pathlib.Path, dialects: dict[str, str]) -> list[dict]:
    out = []
    for path in sorted((service / "evals" / "usecases").glob("*/questions.jsonl")):
        for line in path.read_text().splitlines():
            line = line.strip()
            if not line:
                continue
            case = json.loads(line)
            sql = case.get("gold_sql")
            if not sql:
                continue
            out.append(
                {
                    "sql": sql,
                    "dialect": dialects.get(case.get("source", ""), "tsql"),
                    "category": MUST_PARSE,
                    "from": f"evals/{path.parent.name}:{case.get('id', '?')}",
                }
            )
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--service", required=True, type=pathlib.Path)
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--out", default="testdata/service", type=pathlib.Path)
    a = ap.parse_args()

    service = a.service.expanduser()
    repo = pathlib.Path(__file__).resolve().parent.parent
    sys.path.insert(0, str(a.sqlglot.expanduser()))
    sys.path.insert(0, str(repo / "harness"))
    from oracle import dump, dump_tokens, pinned_commit, reference_commit  # noqa: PLC0415

    actual, pinned = reference_commit(a.sqlglot.expanduser()), pinned_commit(repo)
    if actual != pinned:
        raise SystemExit(f"reference is at {actual[:12]} but NOTICE pins {pinned[:12]}")

    dialects = dialects_by_source(service)
    cases = (
        from_evals(service, dialects)
        + from_python_guard_tests(service / "tests" / "test_sqlguard.py", "tsql")
        + from_python_guard_tests(service / "services" / "conformance" / "run.py", "tsql")
        + from_go_guard_tests(service / "services" / "warehouse-query-go" / "sqlguard_test.go", "tsql")
    )

    # The same statement appears in several suites by design -- the guard
    # corpus is deliberately duplicated across implementations. Count it once,
    # keeping the strongest category it was filed under.
    strength = {MAY_REFUSE_UNPARSED: 0, MUST_PARSE_TO_REFUSE: 1, MUST_PARSE: 2}
    merged: dict[tuple[str, str], dict] = {}
    for case in cases:
        if not case["sql"].strip():
            continue
        key = (case["dialect"], case["sql"])
        keep = merged.get(key)
        if keep is None or strength[case["category"]] > strength[keep["category"]]:
            if keep is not None:
                case["from"] = keep["from"] + ", " + case["from"]
            merged[key] = case
        elif keep is not None and case["from"] not in keep["from"]:
            keep["from"] += ", " + case["from"]

    ordered = sorted(merged.values(), key=lambda c: (c["category"], c["dialect"], c["sql"]))

    # The reference's tree, where the reference can produce one, so this
    # corpus checks the port is RIGHT and not merely willing.
    unreadable = 0
    for case in ordered:
        try:
            case["tree"] = dump(case["sql"], case["dialect"])
            case["tokens"] = dump_tokens(case["sql"], case["dialect"])
        except Exception:  # noqa: BLE001 -- a statement the reference cannot read is recorded as such
            case["reference_parsed"] = False
            unreadable += 1
        else:
            case["reference_parsed"] = True

    head = subprocess.run(
        ["git", "-C", str(service), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()

    a.out.mkdir(parents=True, exist_ok=True)
    (a.out / "corpus.json").write_text(
        json.dumps({"service": head, "reference": actual, "cases": ordered}, indent=1) + "\n"
    )

    by_category: dict[str, int] = {}
    for case in ordered:
        by_category[case["category"]] = by_category.get(case["category"], 0) + 1
    print(f"data agent service {head[:12]}: {len(ordered)} statements")
    for category in (MUST_PARSE, MUST_PARSE_TO_REFUSE, MAY_REFUSE_UNPARSED):
        print(f"  {category:24} {by_category.get(category, 0)}")
    if unreadable:
        print(f"  {unreadable} that the REFERENCE cannot parse either (recorded, not hidden)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
