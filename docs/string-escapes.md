# The string-escape campaign: how it was ported

**Done, and kept as the record of how.** The reference's 420 commits between
`ceb5111421e9` and `78b24a744` (v30.24.0-53) were split in two on purpose:
180 were a re-pin (`c7778cf56`, v30.21.0-9) and the remaining 240 were this —
a deliberate, cross-dialect campaign to **decode string escapes**, which is a
feature rather than maintenance: a different model of what an escape *is*.

It was worth its own port because a decoded escape is a different **literal
value**. `SELECT 'a\nb'` in MySQL is a three-character string holding a
newline, not the four characters `a`, `\`, `n`, `b`, so trees change too, not
only the tokenizer. Upstream marked it breaking, and several dialects gained
byte-literal shapes BigQuery already had. Carried in on the back of a re-pin,
none of that would have been bisectable.

## The commits

| PR | what it changed |
|---|---|
| #8465 | MySQL tokenizer rules (backslash escapes, quoting) |
| #8471 | MySQL decodes backslash string escapes |
| #8478 | Hive decodes backslash string escapes |
| #8479 | BigQuery decodes string escapes and preserves byte literals |
| #8484 | PostgreSQL, DuckDB, Redshift and ClickHouse string escapes |
| #8606 | Oracle alternative quoting and national literals |

The first of them is `15a77f1d8`.

## What replaced `escape_follow_chars`

The port had one set, one rule, for every dialect: `ESCAPE_FOLLOW_CHARS`,
mirrored as `Config.EscapeFollowChars`. It is gone. In its place:

- per-dialect `STRING_ESCAPES`, `BYTE_STRING_ESCAPES`,
  `IDENTIFIER_ESCAPES` and `STRING_ESCAPES_ALLOWED_IN_RAW_STRINGS`;
- `string_escapes`, `byte_string_escapes`, `identifier_escapes`,
  `unescaped_sequences`, `numeric_escapes`, `drop_unknown_escapes`,
  `numeric_escapes_are_bytes`, `byte_strings_are_bytes` and
  `lone_surrogate_replacement`;
- `ESCAPED_SEQUENCES` on the write side, including control characters
  formatted as `\xNN`, `\uNNNN` or `\NNN` depending on the dialect's
  `numeric_escapes` spelling.

The configuration is probed from the reference's `TokenizerCore` rather than
transcribed, and Snowflake's and Oracle's tokenizer configurations are
generated now — they had been copied from the neutral config, so Snowflake
decoded nothing and Oracle had no `q'[...]'`.

## How it was ported

In the four steps the plan named, with the oracle and the differential as the
control for each: the escape model into `Config` and the tokenizer, the decode
step, byte strings and the surrogate replacement, then `NOTICE` moved to
`78b24a744` and `make oracle` re-run.

The differential is what closed it: 12,036 statements parse to the reference's
own tree and are written back to its own string, with nothing unparsed,
mismatched, refused or written differently. The campaign also taught the
generator the decisions that came with it — QUALIFY rewritten where a dialect
has none, FILTER folded into the aggregate where a dialect has none, and
original call names kept exactly where the dialect keeps them.
