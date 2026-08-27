# Upstream issues

Behaviour in the reference (sqlglot) that looks wrong, found while porting.

Nothing here is worked around. The port reproduces the reference exactly --
that is the whole point of it -- so a bug recorded here is a bug the port has
too, on purpose. The entry exists so the divergence is *known* rather than
discovered later by an engine, and so the port's own behaviour can be changed
deliberately if the reference ever fixes it.

Each entry names how it was found. The execution oracle
(`harness/execute.py`) is the only harness here that can find this class at
all: a tree differential proves the port agrees with sqlglot, never that
sqlglot is right.

---

## sqlglot rewrites DuckDB's reversing slice into a one-element slice

**Found by:** the execution oracle, first run.

DuckDB writes "reverse this list" as `[:-:-1]`. The bare `-` is an omitted
bound; DuckDB needs a spelling for it because `[::-1]` is a *parse error*
there -- `::` is the cast operator.

sqlglot reads that omitted bound as `-1`:

```
in   SELECT ([1,2,3])[:-:-1]      -> [3, 2, 1]
out  SELECT ([1, 2, 3])[:-1:-1]   -> [3]
```

Both statements parse, both run, and the round trip is stable -- so every
string- and tree-level check in this repository passes. Only running them
shows that one reverses a list and the other returns its last element.

The port currently refuses this statement (the subscript rewrite is not
ported), so it cannot yet emit the wrong SQL. The oracle catches it via the
reference's own rendering, which is checked alongside the port's for exactly
this reason.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot turns PostgreSQL's binary and hex INTEGER literals into BIT STRINGS

**Found by:** the execution oracle, first PostgreSQL run.

PostgreSQL 16 added `0b`/`0o`/`0x` integer literals, with `_` allowed as a
digit separator. sqlglot rewrites them to the `b'…'` / `x'…'` bit-string
syntax, which is a different type and, for `0b`, a different value:

```
in   SELECT 0b1010   -> 10       integer
out  SELECT b'1010'  -> '1010'   bit

in   SELECT 0x1_F    -> 31       integer
out  SELECT x'1_F'   -> error: "_" is not a valid hexadecimal digit
```

The first is the dangerous one: it runs, returns a value, and the value is
wrong.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot rewrites DATE_PART to EXTRACT, changing the result type

**Found by:** the execution oracle, first PostgreSQL run.

`DATE_PART` returns `double precision`; `EXTRACT` returns `numeric`. The
rewrite is otherwise faithful and the numbers are equal, but the type a
caller receives changes:

```
SELECT pg_typeof(DATE_PART('epoch', now()))     -> double precision
SELECT pg_typeof(EXTRACT(epoch FROM now()))     -> numeric
```

Where the field is an expression rather than a bare word, the rewrite does
not run at all — `EXTRACT` takes an identifier there, not a value:

```
in   SELECT DATE_PART('isodow'::varchar(6), current_date)          -> 1.0
out  SELECT EXTRACT(CAST('isodow' AS VARCHAR(6)) FROM CURRENT_DATE) -> syntax error
```

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot rewrites PostgreSQL's date_add to `+`, dropping the time zone

**Found by:** the execution oracle, first PostgreSQL run.

```
SELECT pg_typeof(date_add(current_date, interval '7' day))  -> timestamp with time zone
SELECT pg_typeof(CURRENT_DATE + INTERVAL '7 DAY')           -> timestamp without time zone
```

Same instant in the session's zone, different type — and therefore different
behaviour once the result crosses a zone boundary.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot folds two satisfiable comparisons over ANY(...) into FALSE

**Found by:** the execution oracle, on the port's own simplify output — and
then confirmed against the reference, which does the same.

```
in   ... WHERE 1 <= ANY(col) AND 2 = ANY(col)   -> the row
out  ... WHERE FALSE                            -> nothing
```

With `col` = `ARRAY[1, 2, 3]` both predicates are TRUE in PostgreSQL, so the
row qualifies.

The range rule reasons about two comparisons that share an operand: `x > 1
AND x < 1` really is FALSE. It reads each comparison as `column OP constant`,
which `sort_comparison` normally arranges — but `sort_comparison` explicitly
leaves a SubqueryPredicate on the right, and `ANY(col)` is one. So the two
comparisons arrive as `1 <= X` and `2 = X`, the rule compares the constants
in the wrong orientation, and concludes they contradict.

They do not contradict even under that reading: `X >= 1 AND X = 2` is
satisfied at 2. And `ANY` is not a single value at all, which is the deeper
reason the rule should not fire here.

The port reproduces this, on purpose. It is recorded in
`testdata/execution.json` so the oracle does not report it every run.

**Reference:** sqlglot @ ceb5111421e9.

## sqlglot writes a JSON path key without escaping the quote in it

`JSON_EXTRACT_PATH` folds its keys back out as separate string arguments, and
a key holding a single quote is written into that string unescaped. The result
does not tokenize, so the reference cannot read back what it just wrote:

```
in   SELECT JSON_EXTRACT_PATH(col, 'fr''uit')
out  SELECT JSON_EXTRACT_PATH(col, 'fr'uit')
back Error tokenizing 'SELECT JSON_EXTRACT_PATH(col, 'fr'uit''
```

The same key IS escaped when the path is written as one `'$...'` string, so
this is the per-argument form alone.

**This one the port does NOT reproduce**, which is a departure from the rule
followed everywhere else here, and the reason is that both alternatives were
worse. Reproducing it means emitting SQL that does not parse -- from a guard
whose whole job is to hand an executor something safe to run. Escaping it
means writing a statement the reference writes differently, which is the one
thing the differential exists to prevent.

So the port REFUSES a folded key holding a quote: `cannot generate SQL for
JSONExtract over a key holding a quote`. It reads the statement, and declines
to write it. Two corpus statements land here.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot writes a T-SQL DELETE ... OUTPUT it cannot read back

**Found by:** porting UPDATE, DELETE and MERGE.

T-SQL spells RETURNING as OUTPUT and writes it early -- straight after the
verb in a DELETE, in front of the FROM. Its own parser then expects a table
name there and refuses:

```
in   DELETE FROM x WHERE y > 1 RETURNING a      (read as postgres)
out  DELETE OUTPUT a FROM x WHERE y > 1         (written as tsql)
     sqlglot.errors.ParseError: Expected table name but got OUTPUT
```

The same clause round-trips fine in an UPDATE, where it is written after the
SET list; only the DELETE position is unreadable.

Nothing is worked around. The port's `parseDelete` reads no RETURNING in that
position -- not to avoid the bug, but because there is no reference tree for
it to agree with, so reading one would agree with nothing. `writeDelete`
still places the clause where the reference places it.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot writes a bare `CREATE TABLE` for a Databricks table with a UNIQUE constraint

**Found by:** porting the table-level constraints.

Databricks has no UNIQUE constraint, and the reference drops it. On a COLUMN
that is a lossy but coherent rewrite -- the table is created without the
guarantee. On the TABLE it takes the whole definition with it:

```
in   CREATE TABLE z (a INT UNIQUE)          (read as the neutral dialect)
out  CREATE TABLE z (a INT)                 (written as databricks)

in   CREATE TABLE z (a INT, UNIQUE (a))
out  CREATE TABLE                           -- no name, no columns
```

The second is not a table at all. The first is worse in a quieter way: it
runs, and the table it makes permits duplicates the statement said it should
not.

The port refuses both rather than reproducing either -- the only place in this
port that declines to follow the reference. Dropping a constraint is not a
spelling difference, and a guard that reports "this creates a unique index"
would be reporting something the emitted SQL does not do.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot drops the quotes from a database name in ATTACH and DETACH

**Found by:** porting DuckDB's ATTACH, DETACH and INSTALL.

The name a DETACH names, and the name of each ATTACH option, are read with
`_parse_var`, which takes a quoted identifier's TEXT and forgets that it was
quoted. Writing the tree back spells the name bare:

```
in   DETACH "My DB"                    (duckdb)
out  DETACH My DB                      -- which sqlglot then cannot read

in   ATTACH 'f' AS x ("Q" 1)           (duckdb)
out  ATTACH 'f' AS x (Q 1)             -- reads, but names something else

in   INSTALL x FROM "q"                (duckdb)
out  INSTALL x FROM q                  -- same, in a third position
```

The first is unreadable. The second is the quieter one: it parses, and in
DuckDB an unquoted name is folded to lower case, so `"Q"` and `Q` are not the
same setting.

The value of an option keeps its quotes -- `ATTACH 'f' (TYPE "sq lite")`
round-trips -- because that side is read with `_parse_field`. So the loss
follows the READER used, not the statement: every position that reaches for a
bare word loses the quotes, and the one that reaches for a field keeps them.

The port refuses a quoted name in either position and reads it everywhere the
reference keeps it. There is no tree to agree with otherwise: the SQL that
comes back names a different database.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot writes Python's `None` into an ANALYZE, and upper-cases a table it analyses

**Found by:** porting ANALYZE.

Two separate losses in the same statement, both in positions the port now
refuses.

`BUFFER_USAGE_LIMIT` is kept as one option string with its number built into
the text: `f"BUFFER_USAGE_LIMIT {self._parse_number()}"`. Where no number
follows, the interpolation writes the Python value:

```
in   ANALYZE BUFFER_USAGE_LIMIT TBL        (postgres)
out  ANALYZE BUFFER_USAGE_LIMIT None TBL   -- which sqlglot cannot read back
```

Separately, a table with a column list is read as a function CALL, and a call
takes the name's own casing rules -- so the name is written back in a
different case:

```
in   ANALYZE a.b(c)                        (postgres)
out  ANALYZE a.B(c)

in   ANALYZE "a"."b"(c)
out  ANALYZE "a"."B"(c)
```

The second is the worse of the two. It round-trips, so nothing complains, and
in PostgreSQL a quoted name is case-SENSITIVE: `"b"` and `"B"` are two
different tables. The unquoted form is harmless in PostgreSQL, which folds to
lower case, and is not harmless everywhere.

The port reads `ANALYZE TBL(col1, col2)` -- the Anonymous call and all,
because that is the tree the reference builds -- and refuses the qualified
form and the empty BUFFER_USAGE_LIMIT.

**Reference:** sqlglot @ ceb5111421e9.

---

## sqlglot writes a T-SQL `TOP -1` it cannot read back

**Found by:** the generator fuzzer, twice, on the same rule.

T-SQL needs parentheses around a row limit that is not a plain number, and
`limit_sql` puts them there for anything `is_number` says no to. `is_number`
is true for a negated literal, so a negative limit is written bare:

```
in   SELECT x FROM t LIMIT -1          (read as the neutral dialect)
out  SELECT TOP -1 x FROM t            (written as tsql)
```

which sqlglot then refuses to parse. A string limit is handled correctly --
`LIMIT ''` becomes `TOP ('')` -- because a string literal is not a number.

This is the one place the port's writer does not follow the reference: it
parenthesises the negative too, writing `SELECT TOP (-1) x FROM t`. The
alternative is emitting SQL that neither the reference nor this port can read,
and the port holds itself to reading back everything it writes -- that
property is a fuzz target, so following the reference here would fail its own
gate.

Nothing else changes. A count that is a plain number, including a float, is
still written bare.

**Reference:** sqlglot @ ceb5111421e9.

---

## A note on cost: the type annotator is quadratic over a long chain

**Not an upstream issue -- a limit of this port, recorded here so it is not
rediscovered.**

`annotate` works a binary operator's type out by annotating both operands
again, and it does that whether or not they already carry a type. `AnnotateFully`
walks bottom-up and annotates every node, so a chain of n operators is
annotated O(n^2) times.

It shows up on a subscript, because reading one in a dialect that numbers from
1 annotates the index to decide whether to shift it:

```
A[0*0*0* ... *0]      2000 terms, postgres:   ~570ms
                      1000 terms:             ~140ms
                       500 terms:              ~38ms
```

-- four times the work for twice the input.

The obvious fix is to read a child's stamped type instead of recomputing it,
and it is not safe as written: `AnnotateFully` stamps the type the DUMP is
held to, which converts NULL to UNKNOWN, while an operator above needs the
NULL. `NULL + 1` is an INT and `x + 1` is UNKNOWN, and the two would become
the same answer. Memoising the RAW result separately is the way, and it is a
change to the annotator rather than to a caller.

A related fix has already landed: `simplifyNode` copied each node's whole
subtree before replacing every child of the copy, which was the larger half of
the same statement's cost -- three seconds down to under one.

**Reference:** sqlglot @ ceb5111421e9.
