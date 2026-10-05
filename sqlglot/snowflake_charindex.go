package sqlglot

import "maps"

// Snowflake's CHARINDEX looks for the needle in the haystack, then an
// optional start. The node records that a negative start is clamped, and
// that flag is not written back.
func init() {
	spellSnowflakeCharindex(parserTables["snowflake"], "StrPosition")
}

func spellSnowflakeCharindex(tables *ParserTables, class string) {
	if tables == nil || class == "" {
		return
	}
	readSnowflakeCharindex(tables, class)
	writeSnowflakeCharindex(tables, class)
}

func readSnowflakeCharindex(tables *ParserTables, class string) {
	functions := maps.Clone(tables.Functions)
	functions["CHARINDEX"] = FuncSpec{Class: class, Args: charindexArguments()}
	tables.Functions = functions
}

func charindexArguments() []FuncArg {
	slots := []struct {
		key   string
		index int
		flag  bool
	}{
		{key: "this", index: 1},
		{key: "substr", index: 0},
		{key: "position", index: 2},
		{key: "clamp_position", index: -1, flag: true},
	}
	args := make([]FuncArg, 0, len(slots))
	for _, slot := range slots {
		arg := FuncArg{Key: slot.key, Index: slot.index}
		if slot.flag {
			arg.Const = true
		}
		args = append(args, arg)
	}
	return args
}

func writeSnowflakeCharindex(tables *ParserTables, class string) {
	forms := maps.Clone(tables.SyntaxSQL)
	prior := append([]SyntaxTemplate(nil), forms[class]...)
	placed := []SyntaxTemplate{
		charindexTemplate(
			[]string{"substr", "this", "position", "clamp_position"},
			[]string{"substr", "this", "position"},
			"CHARINDEX({substr}, {this}, {position})",
		),
		charindexTemplate(
			[]string{"substr", "this", "clamp_position"},
			[]string{"substr", "this"},
			"CHARINDEX({substr}, {this})",
		),
	}
	forms[class] = append(placed, prior...)
	tables.SyntaxSQL = forms
}

func charindexTemplate(keys, marked []string, text string) SyntaxTemplate {
	return SyntaxTemplate{
		Keys:     keys,
		Marked:   marked,
		Required: []FuncConst{{Key: "clamp_position", Value: true}},
		Template: text,
	}
}
