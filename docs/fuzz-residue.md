# Fuzzing residue

What `make fuzz-differential` still reports, and why each class is not a
statement a caller can write.

The corpus is saturated — 10,871 statements, 0 unparsed, 0 mismatched — so it
can no longer find anything, and every bug found since has come from outside
it: `CONNECT BY … PRIOR` from the first consumer, and two rules from fuzzing
(the empty table qualifier, and a select list read as a CSV). Those two are
fixed. This file is the other half of the fuzzer: the classes that remain.

Its purpose is to be **the floor**. A future session that reports a class not
named here has found something. One that reports only these has not.

**Compare by CLASS, never by total.** The fuzzer explores differently on every
run — two 240-second sessions over the same build produced 1,151 and 555
divergences, which is a difference in what it chose to mutate, not in the
port.

## How a session is read

    make PYTHON=python3.11 fuzz-differential SECONDS=240

`FuzzParsedStatementsForTheDifferential` collects `(dialect, sql)` pairs the
port PARSES, the oracle builds the reference's expectations for them, and
`TestAgainstReference` diffs every tree. The fuzzer mutates real SQL into
fragments, so most candidates are not statements at all — that is what the
classes below are.

## The classes, and why each is a fragment

Counts are from one run (555 divergences) and are illustrative.

| class | ~ | example | why it is not writable SQL |
|---|--:|---|---|
| `ScopeResolution -> Cast` | 198 | `00::A`, `$::"0\`"` | a cast of a NUMBER, or of a lone `$`, to a type named `A`; the reference reads the two-part spelling as a user-defined type |
| `Alias -> Column` | 64 | control characters | mutated names |
| `Literal -> Literal` | 59 | `00[0][0]` | subscripting a small integer |
| `Var -> Column` | 47 | `SET 0=0X` | a SET whose value is a digit-initial bare word; `SET x = y` is right |
| `HexString -> Identifier` | 33 | `:00A.0XA0` | a leading colon |
| `Column -> Dot` | 28 | `AF.*.A` | a `*` in the middle of a dotted name |
| `Literal -> JSONPath` | 28 | `0->.0.` | an arrow with no left operand and a trailing dot |
| `Alias -> Array` | 8 | `[: :]` | an unterminated slice with no members |
| `Anonymous -> All` | 7 | `0<All(0)` | `ALL` applied to a CALL, not a query |
| `Paren -> Subquery` | 6 | `((FROM A)A)` | a FROM-first query with no projection, aliased |
| `Command -> Set` | 4 | `SET x== 9` | a doubled `=` |
| `Identifier -> DataType` | 5 | `UNION<A>` | a type-parameter list after `UNION` |
| `Alias -> EQ` | 4 | `SELECT 0X=0` | a comparison whose left side is the hex-looking word `0X` |
| others | <5 each | | `HexString -> Var`, `RawString -> Var`, `Placeholder -> Struct` (`{}`), `ByteString -> Var`, `IfBlock -> If`, `Set -> Var`, `Paren -> Tuple` — all single mutations |

Roughly 40% of every session's input contains a control character, which is
worse than a fragment: sqlglot's answers there are not a specification, and no
caller writes them.

## One generator gap, recorded rather than fixed

For Databricks, the port writes an `APPLY` back as it was written:

```
port       SELECT * FROM t OUTER APPLY G(1)
reference  SELECT * FROM t JOIN LEFT JOIN LATERAL G(1) ON TRUE
```

The reference's spelling is not valid SQL. The port's TREE agrees with the
reference's — the join records `on: TRUE`, which is what the reference's tree
records — so the tree differential cannot see this, and the corpus holds no
Databricks `APPLY`. Reproducing the reference's spelling would write a
statement no engine reads, so it is left as it stands and named here rather
than fixed.

## Never a bug

`THEIRS` in the generator fuzz (`make fuzz`) means sqlglot does the same
thing — the property asserted was wrong, not the port. A full generator
session reports thousands of these and, so far, **zero** `YOURS`.
