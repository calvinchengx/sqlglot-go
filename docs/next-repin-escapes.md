# The next re-pin: the escape campaign

This branch re-pins the reference from `ceb5111421e9` to `c7778cf56`
(v30.21.0-9) — **180 of the 420 commits** between the old pin and
`origin/main` at `78b24a744` (v30.24.0-53).

The remaining 240 are not maintenance. They contain a deliberate,
cross-dialect campaign to **decode string escapes**, and that is a feature:
a different model of what an escape *is*, one the port mirrors structurally.
It deserves its own port, with its own tests, rather than being carried in on
the back of a re-pin nobody can bisect.

## The commits

| PR | what it changed |
|---|---|
| #8465 | MySQL tokenizer rules (backslash escapes, quoting) |
| #8471 | MySQL decodes backslash string escapes |
| #8478 | Hive decodes backslash string escapes |
| #8479 | BigQuery decodes string escapes and preserves byte literals |
| #8484 | PostgreSQL, DuckDB, Redshift and ClickHouse string escapes |
| #8606 | Oracle alternative quoting and national literals |

The first of them is `15a77f1d8`; everything from there to the pin is what is
deferred.

## What the port has today

`Tokenizer.ESCAPE_FOLLOW_CHARS` — mirrored as
`Config.EscapeFollowChars` (`sqlglot/config.go`) and consulted at
`sqlglot/tokenizer.go` when a `\` is followed by a character in the set. One
set, one rule, for every dialect.

## What replaced it

`escape_follow_chars` is gone. In its place:

- per-dialect `STRING_ESCAPES`, `BYTE_STRING_ESCAPES`,
  `IDENTIFIER_ESCAPES` and `STRING_ESCAPES_ALLOWED_IN_RAW_STRINGS`;
- `TokenizerCore` fields `string_escapes`, `byte_string_escapes`,
  `identifier_escapes`, `unescaped_sequences`, `numeric_escapes`,
  `drop_unknown_escapes`, `numeric_escapes_are_bytes`,
  `byte_strings_are_bytes` and `lone_surrogate_replacement`;
- `ESCAPED_SEQUENCES` built in the dialect metaclass, including control
  characters formatted as `\xNN`, `\uNNNN` or `\NNN` depending on the
  dialect's `numeric_escapes` spelling.

`Tokenizer` now delegates to `TokenizerCore` (`sqlglot/tokenizer_core.py`),
so the probe reads the configuration from there rather than off `Tokenizer`.

## What it changes

A decoded escape is a different **literal value**: `SELECT 'a\nb'` in MySQL
is a three-character string with a newline, not the four characters
`a`, `\`, `n`, `b`. That means trees change too, not only the tokenizer — the
campaign is marked breaking upstream, and several dialects gain byte-literal
shapes BigQuery already had.

## Shape of the work

1. Port the escape model into `Config` and the tokenizer (sets and mappings
   per dialect, all probed from `TokenizerCore`).
2. Port the decode step: `unescaped_sequences`, `numeric_escapes` (decimal /
   hex / octal per dialect), unknown-escape handling and raw strings.
3. Port byte strings and the surrogate replacement.
4. Move `NOTICE` to `78b24a744` (or later), run `make oracle`, and work the
   differential the same way this branch did: refusals first, then the
   statements that already passed.

The oracle and the differential are the control for all four steps; nothing
here should be landed without them.
