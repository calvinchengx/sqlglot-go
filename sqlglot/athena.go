package sqlglot

import "strings"

// Athena is not a dialect so much as a ROUTER: AWS built it over Hive and
// Trino and sends each statement to one or the other, because the two
// disagree about quoting, about DDL, and about what a function is called.
//
// So a statement is read by whichever engine would run it, and written back by
// whichever engine would run it -- and the two decisions are made in different
// places. The reference decides the READ from the first TOKENS (its tokenizer
// re-tokenizes once it knows), and the WRITE from the TREE, because by then
// the tokens are gone. The two rules are not the same rule, and both are
// written out here as they are there.

// athenaReadsAsHive reports which engine would run a statement, from the
// tokens the combined tokenizer produced
// (sqlglot/dialects/athena.py, _tokenize_as_hive).
func athenaReadsAsHive(tokens []Token) bool {
	if len(tokens) < 2 {
		return false
	}
	first, second := tokens[0], tokens[1]
	rest := tokens[2:]

	firstText := strings.ToUpper(first.Text)
	if first.Type == TokDESCRIBE || first.Type == TokSHOW || firstText == "MSCK REPAIR" {
		return true
	}
	if first.Type == TokALTER || first.Type == TokCREATE || first.Type == TokDROP {
		secondText := strings.ToUpper(second.Text)
		if secondText == "DATABASE" || secondText == "EXTERNAL" || secondText == "SCHEMA" {
			return true
		}
		// CREATE VIEW and DROP VIEW go to Trino, because a view is a query.
		if second.Type == TokVIEW {
			return false
		}
		// Anything else is Hive unless a SELECT is hiding in it: a CREATE
		// TABLE AS SELECT is Trino's.
		for _, t := range rest {
			if t.Type == TokSELECT {
				return false
			}
		}
		return true
	}
	return false
}

// athenaWritesAsHive reports which engine would run a tree, which is a
// different question from which one read it
// (sqlglot/generators/athena.py, _generate_as_hive).
func athenaWritesAsHive(e *Expression) bool {
	if e == nil {
		return false
	}
	switch e.Class {
	case "Create":
		kind, _ := e.Args["kind"].(string)
		if kind == "TABLE" {
			// CREATE EXTERNAL TABLE is Hive.
			if properties, _ := e.Args["properties"].(*Expression); properties != nil {
				if len(properties.FindAll("ExternalProperty")) > 0 {
					return true
				}
			}
			// Any CREATE TABLE that is not CREATE TABLE ... AS <query> is
			// Hive -- except a CTAS with a leading USING EXTERNAL FUNCTION
			// clause, which is Trino's (_generate_as_hive).
			expr, _ := e.Args["expression"].(*Expression)
			if expr != nil && expr.Class == "Command" {
				if word, _ := expr.Args["this"].(string); strings.EqualFold(word, "USING") {
					return false
				}
			}
			return expr == nil || !isQuery(expr)
		}
		// CREATE VIEW is Trino; CREATE SCHEMA, CREATE DATABASE and the rest
		// are Hive.
		return kind != "VIEW"
	case "Alter", "Drop", "Describe", "Show":
		if e.Class == "Drop" {
			if kind, _ := e.Args["kind"].(string); kind == "VIEW" {
				return false
			}
		}
		return true
	}
	return false
}
