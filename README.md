# sqlglot-go

A Go port of [sqlglot](https://github.com/tobymao/sqlglot), verified
statement-by-statement against the Python reference.

**What it is:** sqlglot's architecture — tokenizer, data-driven parser,
expression tree, dialects as override sets — in pure Go, built outward from
what a read-only SQL guard needs. Every construct in a parsed statement is a
visible node; a construct the port does not know is a parse error, never a
silent pass.

**What it is not:** sqlglot in Go. No transpiler, no optimizer, no DML/DDL
parsing beyond recognising a statement well enough to refuse it, and four
dialects (T-SQL/Fabric, PostgreSQL, DuckDB, Databricks) rather than 35. The
scope is deliberate — see `docs/17-sqlglot-go.md` in
[data-agent-service](https://github.com/calvinchengx/data-agent-service),
whose Go executor is the first consumer.

## Coverage against the reference

<!-- coverage:start -->
| Dialect | Matched | Of | Unparsed | Mismatched |
|---|---|---|---|---|
| neutral | 0 | 980 | 980 | 0 |
| tsql | 0 | 224 | 224 | 0 |
| postgres | 0 | 385 | 385 | 0 |
| duckdb | 0 | 384 | 384 | 0 |
| databricks | 0 | 144 | 144 | 0 |
<!-- coverage:end -->

Reference: sqlglot `ceb5111421e9` (v30.17.0-64). **Mismatched is the number
that matters** and must be zero: it counts statements the port parsed into a
*different* tree than the reference. Unparsed is the honest size of the gap.

## How it is verified

`testdata/expected/` holds 2,117 statements — sqlglot's own `identity.sql`
and the four dialect suites — each with the tree the reference produced, in
`serde.dump()` format. `go test ./...` parses every one with the port, dumps
it the same way, and diffs. The expectations are committed, so the Go tests
need no Python; regenerating them (`make oracle`) refuses to run against any
commit other than the one `NOTICE` pins.

A divergence fails the build. A coverage regression below `testdata/floor.json`
fails the build. The harness proves it can fail (`TestTheHarnessCanTellRightFromWrong`).

```sh
make test       # the differential run
make coverage   # per-dialect numbers
make oracle     # regenerate expectations from the pinned reference
```

## License

Apache 2.0. The tokenizer design, expression model, parser architecture and
dialect pattern derive from sqlglot, © Toby Mao, MIT — reproduced in
`LICENSE.sqlglot` and credited in `NOTICE`.
