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
      "table function", "not queryable", "cross-database". If the parser
      cannot read them, they are still refused, but for the wrong reason, and
      the conformance suite checks the reason.

  must_name_the_statement
      Statements refused for a reason that depends only on WHAT KIND of
      statement it is: a write, or more than one. The parser has to name them,
      not parse them -- which is what Tier 1 means by recognising DDL and DML
      "only far enough to refuse".

  may_refuse_unparsed
      Statements where any refusal is the right answer: the statement does not
      parse and that IS the reason, or the suite asserts no reason at all.
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
MUST_NAME_THE_STATEMENT = "must_name_the_statement"
MAY_REFUSE_UNPARSED = "may_refuse_unparsed"

# Reasons where a refusal is correct however it comes about: the statement is
# malformed, or the suite asserts no particular reason for refusing it. The
# empty string is the second case -- a test that only requires a refusal must
# not outrank another suite that requires a specific one.
UNPARSEABLE_REASONS = {"parse", "empty", ""}

# Reasons that depend on WHAT KIND of statement it is, not on its tree. A
# read-only guard refuses a DROP on sight; naming the statement is enough, and
# building a tree for it would be work with no consumer. This is what Tier 1
# means by recognising DDL and DML "only far enough to refuse".
STATEMENT_KIND_REASONS = {"read-only", "one statement"}


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
    r = reason.strip().lower()
    if r in UNPARSEABLE_REASONS:
        return MAY_REFUSE_UNPARSED
    if r in STATEMENT_KIND_REASONS:
        return MUST_NAME_THE_STATEMENT
    return MUST_PARSE_TO_REFUSE


def from_guard_corpus(path: pathlib.Path) -> list[dict]:
    """The recorded guard corpus, which is where these statements now live.

    They used to be literals inside `tests/test_sqlguard.py`, scraped out of
    its AST. Phase B moved them into this JSON so one file feeds the Python
    guard, the Go guard and the conformance suite -- and the scraper went on
    reading the test module, which still exists and still passes, but no
    longer holds a single statement. The count fell from 144 to 96 and three
    whole categories went to zero, silently, because a source that yields
    nothing looks exactly like a source with nothing to say.
    """
    if not path.exists():
        return []
    cases = json.loads(path.read_text())["cases"]
    out: list[dict] = []
    for case in cases:
        refused = case.get("expect") == "refused"
        out.append(
            {
                "sql": case["sql"],
                "dialect": case.get("dialect", "tsql"),
                "category": categorise(case.get("fragment", "")) if refused else MUST_PARSE,
                "reason": case.get("fragment", ""),
                "from": f"{path.name}:{case.get('fragment') or 'permitted'}",
            }
        )
    return out


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
    from oracle import dump, dump_tokens, pinned_commit, reference_commit, render  # noqa: PLC0415

    actual, pinned = reference_commit(a.sqlglot.expanduser()), pinned_commit(repo)
    if actual != pinned:
        raise SystemExit(f"reference is at {actual[:12]} but NOTICE pins {pinned[:12]}")

    dialects = dialects_by_source(service)

    # Named, so a source that goes quiet is a failure rather than a smaller
    # number. `tests/test_sqlguard.py` emptied itself when the corpus moved to
    # JSON and this script kept reading it for hours, reporting 96 statements
    # where there were 144 and zero in three categories -- while the README
    # went on showing the old figures, because they are only rewritten when
    # someone runs this. A measurement that quietly covers less is the failure
    # mode this whole harness exists to prevent.
    sources = {
        "evals": from_evals(service, dialects),
        "guard corpus": from_guard_corpus(service / "services" / "contract" / "guard_corpus.json"),
        # `services/conformance/run.py` is NOT read: it now derives its own
        # ALLOWED and REFUSED from the corpus above, so scraping it would
        # count the same statements twice. It was in this list until the
        # fail-closed check above reported it empty.
        "go guard tests": from_go_guard_tests(
            service / "services" / "warehouse-query-go" / "sqlguard_test.go", "tsql"
        ),
    }
    empty = [name for name, found in sources.items() if not found]
    if empty:
        raise SystemExit(
            "these sources yielded no statements: "
            + ", ".join(empty)
            + "\nEither the service moved them or this script stopped finding them. "
            "Fix the source rather than the number."
        )
    cases = [case for found in sources.values() for case in found]

    # The same statement appears in several suites by design -- the guard
    # corpus is deliberately duplicated across implementations. Count it once,
    # keeping the strongest category it was filed under.
    strength = {
        MAY_REFUSE_UNPARSED: 0,
        MUST_NAME_THE_STATEMENT: 1,
        MUST_PARSE_TO_REFUSE: 2,
        MUST_PARSE: 3,
    }
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
            case["rendered"] = render(case["sql"], case["dialect"])
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
    for category in (MUST_PARSE, MUST_PARSE_TO_REFUSE, MUST_NAME_THE_STATEMENT, MAY_REFUSE_UNPARSED):
        print(f"  {category:24} {by_category.get(category, 0)}")
    if unreadable:
        print(f"  {unreadable} that the REFERENCE cannot parse either (recorded, not hidden)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
