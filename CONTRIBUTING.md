# Contributing

The rule is one sentence long: **the port must agree with the reference.**

Every statement it parses is compared against what the sqlglot commit named in
`NOTICE` produces, tree for tree and string for string, and that comparison is
the build. A change that is "more correct than sqlglot" fails it exactly as a
wrong change does. If you think the reference is wrong, that is a real thing to
find and it goes in `docs/upstream-issues.md` -- the port still reproduces it,
because a port that quietly improves on its reference is a port whose output
nobody can predict.

## Before you write code

Run the suite once, so you know what passing looks like:

```sh
go test ./...     # the differential: every committed statement, every dialect
go vet ./...
```

That is the whole requirement for changing the port itself: a Go toolchain, no
network, no engine. Everything else in the repository is harness, and the
harness is not what you are being asked to build.

## Making a change

- **A bug fix carries the test that would have caught it.** The test lands
  first, or in the same commit as the fix -- never after it. There is no bug in
  this repository's history that was fixed without one, and a fix without one is
  a fix that will come back.
- **A dialect, a keyword table or a function catalogue is generated, not
  written.** `make oracle` regenerates them from the pinned reference, and CI
  regenerates and diffs them on every pull request. Hand-editing
  `sqlglot/*_gen.go` is the one change that cannot pass.
- **A newly parsed statement goes into the corpus**, so the differential covers
  it from then on: `harness/gen_*.py` writes the expectation, `go test ./...`
  proves the port reproduces it.
- **Refusing is a valid answer.** Where the port cannot reproduce the reference
  exactly, it refuses the statement and says so; it does not approximate.
  `make gaps` prints what it refuses and why.
- **No new dependencies.** The port imports the Go standard library and nothing
  else. That is what makes it work on five platforms with no cgo, and it is not
  negotiable for convenience.

## Running the harness

The port's tests need Go alone. The Python side -- regenerating expectations,
running SQL through an engine -- is the project's environment, not the machine's:

```sh
make sync        # uv + uv.lock: python-dateutil, duckdb, psycopg
make help        # every target, one line each
make lint        # gofmt -l, go vet, golangci-lint (in a container)
```

`pyproject.toml` declares it and `uv.lock` pins it with hashes; bumping a version
is `uv lock --upgrade-package <name>`, never a hand edit of the lock. `make
oracle` additionally needs a checkout of the pinned sqlglot, because the
expectations are that commit's output and nothing else's.

## What an acceptable contribution looks like

- `gofmt` clean, `go vet` clean, `golangci-lint` clean (`make lint`).
- A test for every behaviour change, and a corpus entry for every newly parsed
  statement.
- Generated files regenerated rather than edited.
- No dependency added to `go.mod`.
- A commit message that says *why*. The repository's own history is the style
  guide: a short conventional summary line, then the reasoning, which is the
  part worth reading.
- No attribution trailers or generator footers in commits. A commit reads as
  written by the person who wrote it.

## Pull requests

`main` is protected: a pull request is required, force-pushes and deletion are
blocked, and five checks must be green before a merge -- the differential, the
coverage floor and lint, the regenerated expectations, the execution oracle, and
CodeQL. Doc-only changes run them too; that is the price of a protected branch
and it is worth it.

This is a single-maintainer project, so a pull request is merged by its author
when CI is green rather than held for a review that would never arrive. Say what
you changed and why; the checks are the argument about whether it is right.

## Reporting

- Bugs and enhancement requests: [issues](https://github.com/calvinchengx/sqlglot-go/issues).
- A crash on hostile input, or a statement written with a meaning other than
  the one parsed: **not** an issue -- `SECURITY.md` says where to send it
  privately.
