package sqlglot

import (
	"strings"
	"unicode/utf8"
)

// withComments writes a node's comments in the reference's spelling. A
// separated clause carries them in front (`/* c */ SELECT`). Anything else
// carries them after. A binary operator writes its own, on the operator, so
// they are not written again here.
func (g *generator) withComments(e *Expression, sql string) string {
	if e == nil || sql == "" || g.err != nil || len(e.Comments) == 0 {
		return sql
	}
	if g.commentsBelongOnOperator(e) || e.Class == "SetOperation" {
		return sql
	}
	text := formatComments(e.Comments)
	if text == "" {
		return sql
	}
	if commentSeparated(e.Class) {
		r, _ := utf8.DecodeRuneInString(sql)
		if r == ' ' || r == '\n' || r == '\t' {
			return " " + text + sql
		}
		return text + " " + sql
	}
	return sql + " " + text
}

// commentsBelongOnOperator reports classes whose comments are written on the
// operator rather than after the whole expression. Like and range operators
// go through binary(); Is does too, from its own writer.
func (g *generator) commentsBelongOnOperator(e *Expression) bool {
	class := e.Class
	if class == "Is" {
		return true
	}
	spelled := g.tables.BinarySQL[class]
	if spelled == "" {
		spelled = g.tables.BinaryRangeSQL[class]
	}
	return spelled != ""
}

// commentSeparated is the reference's WITH_SEPARATED_COMMENTS: the comment
// stands before the clause it belongs to.
func commentSeparated(class string) bool {
	switch class {
	case "Command", "Create", "Describe", "Delete", "Drop", "From", "Insert",
		"Join", "MultitableInserts", "Order", "Group", "Having", "Select",
		"SetOperation", "Update", "Where", "With":
		return true
	default:
		return false
	}
}

// commentedOp places comments just after an operator: `+ /* c */`.
func commentedOp(op string, comments []string) string {
	text := formatComments(comments)
	if text == "" {
		return op
	}
	return op + " " + text
}

func formatComments(comments []string) string {
	var b strings.Builder
	for _, comment := range comments {
		if comment == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("/*")
		b.WriteString(sanitizeComment(comment))
		b.WriteString("*/")
	}
	return b.String()
}

// sanitizeComment pads a comment whose ends are not already space, and
// breaks `/*` and `*/` so a generated block comment cannot close early.
func sanitizeComment(comment string) string {
	first, _ := utf8.DecodeRuneInString(comment)
	if strings.TrimSpace(string(first)) != "" {
		comment = " " + comment
	}
	last, _ := utf8.DecodeLastRuneInString(comment)
	if strings.TrimSpace(string(last)) != "" {
		comment += " "
	}
	comment = strings.ReplaceAll(comment, "*/", "* /")
	comment = strings.ReplaceAll(comment, "/*", "/ *")
	return comment
}
