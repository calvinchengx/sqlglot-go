"""Print the reference's tree for one statement, while writing the parser.

    PYTHONPATH=~/opensource/sqlglot python3 harness/show.py "SELECT a FROM t" [dialect]

The port is built to reproduce these trees exactly, so the fastest way to write
a parser rule is to look at what the rule has to produce. Prints the same
serde.dump() records the expectations hold, minus the position metadata the
comparison strips.
"""

from __future__ import annotations

import json
import sys


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__.strip())
        return 2
    sql = sys.argv[1]
    dialect = sys.argv[2] if len(sys.argv) > 2 else None

    import sqlglot

    tree = sqlglot.parse_one(sql, read=dialect)
    records = [{k: v for k, v in rec.items() if k not in ("m", "o")} for rec in tree.dump()]
    for i, rec in enumerate(records):
        print(f"{i:3} {json.dumps(rec, sort_keys=True)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
