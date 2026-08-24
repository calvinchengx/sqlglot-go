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
