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

## Coverage against the reference

<!-- coverage:start -->
| Dialect | Tokens | Trees matched | Of | Unparsed | Mismatched |
|---|---|---|---|---|---|
| neutral | 997/997 | 331 | 997 | 666 | 0 |
| tsql | 233/233 | 33 | 233 | 200 | 0 |
| postgres | 398/398 | 58 | 398 | 340 | 0 |
| duckdb | 392/392 | 100 | 392 | 292 | 0 |
| databricks | 151/151 | 31 | 151 | 120 | 0 |
<!-- coverage:end -->

Reference: sqlglot `ceb5111421e9` (v30.17.0-64). **Mismatched is the number
that matters** and must be zero: it counts statements the port parsed into a
*different* tree than the reference. Unparsed is the honest size of the gap.

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
make coverage   # per-dialect numbers against the reference
make gaps       # why the port refuses what it refuses, most common first
make cover      # test coverage of the port
make oracle     # regenerate expectations and generated tables from the pinned reference
```

## License

Apache 2.0. The tokenizer design, expression model, parser architecture and
dialect pattern derive from sqlglot, © Toby Mao, MIT — reproduced in
`LICENSE.sqlglot` and credited in `NOTICE`.
