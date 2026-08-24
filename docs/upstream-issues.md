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
