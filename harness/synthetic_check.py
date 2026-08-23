"""Print the reference's tokens for escape mechanisms no configured dialect uses.

    PYTHONPATH=~/opensource/sqlglot python3 harness/synthetic_check.py

sqlglot's tokenizer supports identifier escapes, custom escapes gated on the
following character, and raw strings that still honour escapes. None of the
five dialects this port configures switches any of them on, so the corpus can
never reach that code -- and code a port ships unrun is where the next bug
lives. sqlglot/custom_dialect_test.go exercises those paths against tokenizer
configurations built for the purpose; this script is where the expectations in
that test came from, so the claim "it matches the reference" stays checkable
rather than remembered.
"""

from __future__ import annotations

import typing as t

from sqlglot.tokens import Tokenizer

BS = "\\"
Q = "'"
DQ = '"'


class IdentifierEscapes(Tokenizer):
    IDENTIFIER_ESCAPES: t.ClassVar[list] = [BS]


class CustomEscapeFollowChars(Tokenizer):
    STRING_ESCAPES: t.ClassVar[list] = [BS, Q]
    ESCAPE_FOLLOW_CHARS: t.ClassVar[list] = ["n"]


class RawStringsHonouringEscapes(Tokenizer):
    STRING_ESCAPES: t.ClassVar[list] = [BS]
    RAW_STRINGS: t.ClassVar[list] = [("r" + Q, Q)]
    STRING_ESCAPES_ALLOWED_IN_RAW_STRINGS = True


CASES = (
    (IdentifierEscapes, "SELECT " + DQ + "a" + BS + DQ + "b" + DQ),
    (CustomEscapeFollowChars, "SELECT " + Q + "a" + BS + "xb" + Q),
    (CustomEscapeFollowChars, "SELECT " + Q + "a" + BS + BS + "b" + Q),
    (CustomEscapeFollowChars, "SELECT " + Q + "a" + BS + "nb" + Q),
    (CustomEscapeFollowChars, "SELECT " + Q + "a" + BS),
    (RawStringsHonouringEscapes, "SELECT r" + Q + "a" + BS + Q + "b" + Q),
)


def main() -> None:
    for cls, sql in CASES:
        try:
            got: object = [(x.token_type.name, x.text) for x in cls(None).tokenize(sql)]
        except Exception as e:  # noqa: BLE001 -- a refusal is a result here, not a failure
            got = f"error: {type(e).__name__}"
        print(f"{cls.__name__:26} {sql!r:24} {got}")


if __name__ == "__main__":
    main()
