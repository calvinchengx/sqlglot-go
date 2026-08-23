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

These figures are copied here from `testdata/service/coverage.json` by hand and
nothing checks the copy, which is the same shape of defect the corpus itself
just had. Regenerate with `make service && make coverage` before trusting
them.

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
| neutral | 997/997 | 376 | 997 | 621 | 0 |
| tsql | 233/233 | 43 | 233 | 190 | 0 |
| postgres | 398/398 | 63 | 398 | 335 | 0 |
| duckdb | 392/392 | 107 | 392 | 285 | 0 |
| databricks | 151/151 | 31 | 151 | 120 | 0 |
<!-- coverage:end -->

Reference: sqlglot `ceb5111421e9` (v30.17.0-64). **Mismatched is the number
that matters** and must be zero: it counts statements the port parsed into a
*different* tree than the reference. Unparsed is the honest size of the gap.

The port also writes SQL back out, and is held to the reference's own output
string for string: **593 of the statements it parses are written back
identically**, and the guard's own rewrite -- inject a row ceiling, emit --
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

`testdata/expected/` holds 2,171 statements — sqlglot's own `identity.sql`,
the four dialect suites, and a set chosen to reach the lexical corners those
miss — each with the token stream and the tree the reference produced. `go
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
