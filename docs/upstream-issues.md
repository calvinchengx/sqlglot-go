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

## A note on cost: two quadratic walks over a long chain, both fixed

**Not an upstream issue -- a limit of this port, recorded because the fix has
a correctness condition that is easy to get wrong.**

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

The obvious fix -- read a child's stamped type instead of recomputing it -- is
NOT safe: `AnnotateFully` stamps the type the DUMP is held to, and that one
converts NULL to UNKNOWN, while an operator above needs the NULL. `NULL + 1`
is an INT and `x + 1` is UNKNOWN, and the two would become the same answer.

So what is memoised is the RAW answer, on the node, invalidated when its
arguments change and keyed by dialect. `TestAnnotationMemoKeepsTheRawAnswer`
pins exactly that: asking about the NULL first must not change what the sum
answers.

`simplifyNode` was the other half, and the same shape of mistake: it copied
each node's whole subtree and then replaced every child of the copy, once per
node. A copy one level deep is all it needed.

Together:

```
A[0*0*0* ... *0]      2000 terms, postgres:   3.2s  ->  7ms
                      4000 terms:             (unmeasured) -> 22ms
```

-- and the cost is linear in the number of terms now rather than quadratic.
`TestDeepChainDoesNotBlowUp` holds it there.

**Reference:** sqlglot @ ceb5111421e9.

---

## A note on tiers: the JSON ARROW is read one level out in three dialects

**Not an upstream issue -- a limit of this port, found while adding the
operators beside it.**

sqlglot reads `->` and `->>` in two different places depending on the dialect.
In the base parser they are COLUMN_OPERATORS, an accessor tier binding tighter
than arithmetic. PostgreSQL and DuckDB move them into JSON_OPERATORS, read
level with `||`. The difference is visible whenever arithmetic is next to one:

```
1 + x -> 'y'      neutral:   1 + (x -> 'y')      port: (1 + x) -> 'y'
x -> 'y' + 1      neutral:   (x -> 'y') + 1      port: x -> ('y' + 1)
a & b -> c        neutral:   a & (b -> c)        port: (a & b) -> c
```

The port reads them at the bitwise tier in EVERY dialect, so it agrees with
PostgreSQL and DuckDB and is one tier out in the neutral dialect, T-SQL and
Databricks. No statement in the corpus mixes the tiers, which is why the
differential has never said so.

The three operators added beside them -- `#>`, `#>>` and `?` -- are NOT read
that way. Which dialect reads which at the bitwise tier is probed
(`JSONOperatorsAtBitwise`, one entry per operator per dialect), and elsewhere
they are refused rather than read one tier out. Extending the existing
divergence to three more operators would have been the easy half of this
change and the wrong half.

Fixing the arrows means giving the port the accessor tier the reference has,
and gating the arrows on the same probe. It is a change to `parsePostfix`
rather than to a table, which is why it is written down here rather than done
alongside.

**Reference:** sqlglot @ ceb5111421e9.

## A bare name holding a dollar pairs with an earlier one

`$0 A$` is an alias in PostgreSQL: a parameter named 0, aliased `A$`. Written
back as `$0 AS A$`, the two dollars pair and the statement lexes as three
tokens rather than four -- a parameter, a var called `AS`, and a var called
`A$`. There is no alias in it any more.

The reference does this to its own output:

    >>> sqlglot.parse_one("$0 AS A$", read="postgres").sql("postgres")
    '$AS AS A$'

It survives its own round trip only because it carries the comment that
followed the statement, which separates the pair. This port does not model
comments, so it declines to write a bare name holding a dollar once one has
already gone into the output; the name on its own -- `SELECT 1 AS a$b` -- is
unaffected. Found by the generator fuzzer.

## A DATA_DELETION property with anything else in its parentheses never returns

`_parse_data_deletion_property` reads the parenthesised settings with a loop
that advances only when one of two known settings matches:

```python
if self._match(TokenType.L_PAREN):
    while self._curr and not self._match(TokenType.R_PAREN):
        if self._match_text_seq("FILTER_COLUMN", "="):
            prop.set("filter_column", self._parse_column())
        elif self._match_text_seq("RETENTION_PERIOD", "="):
            prop.set("retention_period", self._parse_retention_period())

        self._match(TokenType.COMMA)
```

Anything else inside the parentheses matches neither branch, and the trailing
`_match(COMMA)` does not move either. The cursor never advances, `self._curr`
stays where it is, and the loop runs forever:

    >>> sqlglot.parse_one("CREATE TABLE t (a INT) DATA_DELETION (aaa)")
    # never returns

Found while probing what each property name accepts after it. The property
itself is fine -- `DATA_DELETION=ON` and `DATA_DELETION (FILTER_COLUMN=c)`
both parse -- so this is a missing "else: advance", not a missing feature.
The port's probe skips the name rather than waiting on it.

`_parse_system_versioning_property` has the same loop written the same way, so
`WITH(SYSTEM_VERSIONING=ON(WHATEVER))` never returns either. The port refuses
an unexpected setting there rather than looping.

**Reference:** sqlglot @ ceb5111421e9.
