# sqlglot-go

A Go port of [sqlglot](https://github.com/tobymao/sqlglot), verified
statement-by-statement against the Python reference.

**What it is:** sqlglot's architecture — tokenizer, data-driven parser,
expression tree, dialects as override sets — in pure Go, built outward from
what a read-only SQL guard needs. Every construct in a parsed statement is a
visible node; a construct the port does not know is a parse error, never a
silent pass.

**What it does not cover yet:** transpiler, optimizer, DML/DDL parsing beyond
recognising a statement well enough to refuse it, and 31 of the 35 dialects —
today it speaks four (T-SQL/Fabric, PostgreSQL, DuckDB, Databricks). A full
port is the destination; the order is driven by the first consumer's needs, not
by the library's table of contents. See `docs/17-sqlglot-go.md` in
[data-agent-service](https://github.com/calvinchengx/data-agent-service),
whose Go executor is the first consumer.

## Coverage against data agent service

The SQL the first consumer is actually held to -- the gold answers in its
evaluation suites, the statements its guard permits, and the ones its
adversarial corpus requires it to refuse for a named reason. **This is the
number that decides when the Go guard can be rewritten over a parse tree**, and
it is deliberately a different question from the one below.

<!-- service:start -->
| Category | Parsed | Of |
|---|---|---|
| `must_name_the_statement` | 13 | 13 |
| `must_parse` | 105 | 105 |
| `must_parse_to_refuse` | 29 | 29 |
<!-- service:end -->

**Every category is complete**, which is the condition for rewriting the Go
guard over a parse tree rather than over a token scan.

`may_refuse_unparsed` no longer appears: the single statement in it is
`SELECT * FRO dbo.x`, which the reference rejects too, so there is nothing for
the port to be held to. The category is still extracted and counted -- it is
absent from this table, not from the corpus.

These figures are written from `testdata/service/coverage.json` by
`make readme`, and CI fails if they disagree with what the suite measured. They
were hand-copied until the corpus generator was found reading a file the
statements had moved out of -- reporting 96 where there were 148, while this
table went on showing the old numbers, because a copy nobody checks is a second
source that drifts.

`must_parse` is SQL the guard permits: a gap there is a question the agent
answers today and would stop answering. `must_parse_to_refuse` is SQL the guard
refuses for a reason that depends on the tree -- "table function", "not
queryable", "cross-database" -- where failing to parse still refuses the
statement, but for the wrong reason, and the conformance suite checks the
reason. `must_name_the_statement` is refused for what the statement IS, a write
or two statements, which takes naming rather than parsing. In
`may_refuse_unparsed` any refusal is right.

Regenerate with `make service`; nothing in it is authored here.

## Coverage against the reference

<!-- coverage:start -->
| Dialect | Tokens | Trees matched | Of | Unparsed | Mismatched |
|---|---|---|---|---|---|
| neutral | 997/997 | 464 | 997 | 533 | 0 |
| tsql | 597/597 | 197 | 597 | 400 | 0 |
| postgres | 857/857 | 278 | 857 | 579 | 0 |
| duckdb | 1631/1631 | 741 | 1631 | 890 | 0 |
| databricks | 424/424 | 150 | 424 | 274 | 0 |
<!-- coverage:end -->

Reference: sqlglot `ceb5111421e9` (v30.17.0-64). **Mismatched is the number
that matters** and must be zero: it counts statements the port parsed into a
*different* tree than the reference. Unparsed is the honest size of the gap.

The port also writes SQL back out, and is held to the reference's own output
string for string: **1,682 of the statements it parses are written back
identically and none is written wrongly**, with 86 refused, and the guard's own rewrite -- inject a row ceiling, emit --
lands as `TOP 500` in T-SQL and `LIMIT 500` in DuckDB from the same edit to the
same node. Where a dialect would transform a statement in a way the port does
not perform, the generator **refuses** rather than emit something close: an
executor that quietly wrote a different statement from its counterpart would be
worse than one that declined.

The tokenizer is complete and has no gap tier: every statement the reference
lexes, the port lexes into the same tokens — same types, same text, same line,
column and offsets, same attached comments. A tokenizer has nowhere to
legitimately give up, because the parser above it cannot see what it drops.
The parser is being built outward from `SELECT`. It refuses everything outside
the grammar it has: a construct it does not understand is an `ErrUnsupported`
that counts as unparsed, never a tree that merely looks plausible. That is why
**mismatched is zero at every step** and is the number to watch.

## How it is verified

`testdata/expected/` holds 4,506 statements — sqlglot's own `identity.sql`, its
**whole** dialect suite, and a set chosen to reach the lexical corners those
miss — each with the token stream and the tree the reference produced.

"Whole" is the part worth stating. sqlglot pins a dialect's behaviour two ways:
`validate_identity("…")`, a statement that round-trips, and
`validate_all(…, read={"duckdb": "…"}, write={"tsql": "…"})`, one concept and
how each dialect spells it. Reading only `validate_identity` out of
`test_<dialect>.py` — which is what this harness did at first — missed both the
second form and every file not named after one of our dialects. That was most
of it: the largest single source of DuckDB statements is
`tests/dialects/test_snowflake.py`, and the largest overall is
`tests/dialects/test_dialect.py`, 5,448 lines organised by CONCEPT rather than
by dialect. Harvesting those took the corpus from 2,171 to 4,506 and found
**31 statements the port parsed into a different tree**, including two —
`IS [NOT] DISTINCT FROM` and typed division — that had been found the
expensive way, by fuzzing, while sitting in the reference's own tests all
along.

The function catalogue is probed, not transcribed: each builder is run and the
node it returns is read back. That probe is only as good as the arguments it
runs the builder with, and a builder that INSPECTS what it was handed yields a
spec that is right for a plain column and wrong for real SQL — DuckDB's
`DATE_TRUNC` builds a `DateTrunc` over a DATE cast and a `TimestampTrunc`
otherwise, and `LOWER(HEX(x))` is a `LowerHex`, not a `Lower`. So every spec is
put to a cast, a nested call, a string, a number and a subquery, and kept only
if it survives all of them. A name whose spec does not survive is **refused**,
because a plausible tree the reference never builds is the one thing this port
must not produce. `go
test ./...` runs every one through the port and diffs both. The expectations
are committed, so the Go tests need no Python; regenerating them (`make
oracle`) refuses to run against any commit other than the one `NOTICE` pins.

The keyword and dialect tables the tokenizer reads are generated from the same
pin rather than transcribed, and CI regenerates and diffs them, because a
hand-edited table is a divergence the port has no logic to catch.

A divergence fails the build. A coverage regression below `testdata/floor.json`
fails the build. Test coverage below 95% fails the build; it currently stands
at 100% of statements. The harness proves it can fail
(`TestTheHarnessCanTellRightFromWrong`).

```sh
make test       # the differential run
make coverage   # both coverage numbers
make service    # re-extract the corpus of SQL data agent service is held to
make gaps       # why the port refuses what it refuses, most common first
make cover      # test coverage of the port
make oracle     # regenerate expectations and generated tables from the pinned reference
```

## Working on Windows

The port is pure Go with no dependencies, and the tests need nothing but the Go
toolchain -- the expectations are committed, so there is no Python to install,
no native library, nothing to fetch. CI runs the full differential on **Linux,
macOS and Windows, amd64 and arm64**, and it invokes `go` directly rather than
`make`, so all five platforms are proven by the same commands you would type:

```powershell
go test ./...
go vet ./...
go test ./... -coverpkg=./sqlglot/ -coverprofile=cover.out
go test ./harness/ -run TestGapReport -v
```

**The `Makefile` is convenience, not the build.** Its targets assume GNU make
and a POSIX shell, so on Windows they want WSL or Git Bash. Nothing is lost by
skipping it: `make test` is `go test ./...`, and `make gaps` is the last command
above with the file-and-line prefix stripped.

The remaining targets -- `oracle`, `service`, `lint` -- regenerate the committed
expectations and the generated tables, or lint in a container. Those need
Python, a checkout of the pinned sqlglot, and Docker. The asymmetry is
deliberate: **contributing to the port needs a Go toolchain; maintaining the
oracle needs more.** A contributor on Windows is held to exactly the same
differential as everyone else without installing any of it.

## License

Apache 2.0. The tokenizer design, expression model, parser architecture and
dialect pattern derive from sqlglot, © Toby Mao, MIT — reproduced in
`LICENSE.sqlglot` and credited in `NOTICE`.
