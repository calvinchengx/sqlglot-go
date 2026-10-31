# Security policy

This port reads SQL and writes SQL. It runs none of it, holds no credentials,
and depends on nothing outside the Go standard library -- so the attack surface
is the parser and the generator, and nothing else.

## What counts as a vulnerability

In scope:

- **A crash, a hang, or unbounded memory on input that is only a string.** A
  parser that sits in front of a database and can be made to panic is a denial
  of service against that database. Both fuzzers hunt this class --
  `FuzzParseOneNeverPanics` directly, and the generator's read-back property
  from the other side -- and a report that finds one of their blind spots is
  worth the write-up.
- **A statement the port writes that means something DIFFERENT from the
  statement it read**, especially where the difference drops a condition, widens
  a filter, or changes which rows a query touches. That is this project's own
  correctness bar, and the execution oracle (`harness/execute.py`) is the only
  harness here that can see it.

Not in scope:

- A rendering difference the reference (sqlglot) has too. That is upstream
  behaviour, recorded in `docs/upstream-issues.md`; the port reproduces it on
  purpose, and the fix belongs upstream.
- A statement that is invalid SQL, or that the port refuses to parse. Refusing
  is a supported answer, and it is the answer this port prefers to a guess.
- Anything that needs more than a string handed to the parser: a hostile
  `encoding`, a modified `go.mod`, a forked tree.

## How to report one

Use GitHub's private vulnerability reporting on this repository: **Security ->
Report a vulnerability**, or directly,
`https://github.com/calvinchengx/sqlglot-go/security/advisories/new`.

Please do not open a public issue for anything in the in-scope list. If the
private channel is somehow unavailable, open an issue that says only that you
have a report to make privately -- with no detail in it -- and wait.

A useful report is the slowest to write and the fastest to act on: the exact
input, the dialect it was read with, what the port did, and what you expected.
A test case beats a description, and a statement that also reproduces against
the pinned reference beats both, because it settles whether the bug is the
port's or sqlglot's.

## What happens next

This is a single-maintainer project: acknowledgement is best effort, usually
within a week. A confirmed finding is fixed on `main` with the regression test
first, as everything else here is, and credited in the advisory unless you would
rather not be named.

## Supported versions

The head of `main` is what receives fixes. Tags are cut as the port moves --
`v0.9.0` is the current line -- and no release line is patched separately. If a
specific tag matters to you, say so in the report and the answer will be plain.

## What already runs

- **CodeQL** on every pull request and every push to `main`.
- **OpenSSF Scorecard** weekly and on every push to `main`, published to the
  public Scorecard API and to code scanning.
- **A short fuzz of the parser** in CI on every build, and the differential run
  against the pinned reference on five platforms.
- The repository's **Security** tab carries the current findings of all of it.
