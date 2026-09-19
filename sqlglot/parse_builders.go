package sqlglot

import (
	"fmt"
	"strconv"
	"strings"
)

// Builders that READ the arguments they are handed.
//
// Almost every function node the port builds comes from a signature the probe
// recorded by driving the reference's own builder with placeholder columns.
// That works because almost every builder only moves its arguments into
// slots. A few decide what to BUILD from what is in them -- and a placeholder
// column tells the probe nothing about that, so the recorded signature
// describes a shape the reference never produces for real SQL. Those builders
// are written out here, from the reference, and pinned by tests.

// buildFormat is T-SQL's FORMAT.
//
// The reference reads the format it is given and builds one of two nodes from
// it: a spelling with no date field anywhere in it is a NUMBER format and
// stays as written, and anything else is a time format, rewritten into the
// reference's own spelling on the way in -- through a table of its own when
// the whole format is a single character, and through the dialect's ordinary
// time mapping otherwise.
//
// Enabled by the dialect having a FORMAT_TIME_MAPPING at all, which is the
// same condition that installs this builder in the reference.
func (p *parser) buildFormat(args []*Expression) (*Expression, error) {
	this := argAt(args, 0)
	format := argAt(args, 1)
	culture := argAt(args, 2)

	// Both nodes require a format, and the reference rejects the call outright
	// when it is missing rather than building one without.
	if format == nil {
		return nil, p.unsupported("FORMAT without a format")
	}
	name := format.Name()
	if name == "N" || name == "C" || !hasDateField(name) {
		return New("NumberToStr",
			Arg{"this", this}, Arg{"format", format}, Arg{"culture", culture}), nil
	}
	mapping := p.tables.TimeMapping
	if len([]rune(name)) == 1 {
		mapping = p.tables.FormatTimeMapping
	}
	// A fresh string literal whatever the argument was: the reference takes
	// the NAME off it and builds a literal from that, so a column in this slot
	// comes back as a quoted string.
	return New("TimeToStr",
		Arg{"this", this},
		Arg{"format", New("Literal",
			Arg{"this", formatTime(name, mapping)}, Arg{"is_string", true})},
		Arg{"culture", culture}), nil
}

// hasDateField reports whether a format spelling names any date or time field.
//
// The reference asks a regexp -- `([dD]{1,2})|([mM]{1,2})|([yY]{1,4})|
// ([hH]{1,2})|([sS]{1,2})` -- but it asks it with `search`, so the repetition
// counts never decide anything: one of these characters anywhere is a match.
func hasDateField(s string) bool {
	return strings.ContainsAny(s, "dDmMyYhHsS")
}

// buildDateDiff is T-SQL's DATEDIFF and DATEDIFF_BIG:
// DATEDIFF(unit, start, end) reads as DateDiff(this=end, expression=start,
// unit=Var(unit)) -- the two dates SWAP position, and each is wrapped in
// TimeStrToTime -- except where start is a non-integer NUMBER, which the
// reference keeps bare and does not swap either, the one shape a probe run
// with placeholder columns could never see.
func (p *parser) buildDateDiff(upper string, args []*Expression, bigInt bool) (*Expression, error) {
	if len(args) != 3 {
		return nil, p.unsupported("function " + upper + " with this many arguments")
	}
	unit := args[0]
	start, end := args[1], args[2]
	word := strings.ToUpper(unit.Name())
	if aliases, ok := p.tables.UnitAliases[upper]; ok {
		if full, ok := aliases[word]; ok {
			word = full
		}
	}
	unitVar := New("Var", Arg{"this", word})
	if start != nil && start.Class == "Literal" {
		if isString, _ := start.Args["is_string"].(bool); !isString {
			if isIntegerLiteral(start) {
				return nil, p.unsupported("function " + upper + " over an integer date")
			}
			return New("DateDiff",
				Arg{"this", end}, Arg{"expression", start},
				Arg{"unit", unitVar}, Arg{"big_int", bigInt}), nil
		}
	}
	return New("DateDiff",
		Arg{"this", New("TimeStrToTime", Arg{"this", end})},
		Arg{"expression", New("TimeStrToTime", Arg{"this", start})},
		Arg{"unit", unitVar}, Arg{"big_int", bigInt}), nil
}

// buildDateName is T-SQL's DATENAME(part, date): the part is read through
// TimeMapping merged with FullFormatTimeMapping, so `mm` gives the full month
// name rather than FORMAT's two-digit one, and the date is cast to DATETIME2
// rather than left as written.
// binaryClasses is the reference's own `exp.Binary` subclass set -- a fixed
// reference-level constant, not a per-dialect fact, so it is written out
// once here rather than generated.
var binaryClasses = map[string]bool{
	"Add": true, "Adjacent": true, "And": true, "ArrayContainedBy": true,
	"ArrayContains": true, "ArrayContainsAll": true, "ArrayOverlaps": true,
	"ArrayPosition": true, "BitwiseAnd": true, "BitwiseLeftShift": true,
	"BitwiseOr": true, "BitwiseRightShift": true, "BitwiseXor": true,
	"Collate": true, "Connector": true, "Corr": true, "DPipe": true,
	"Distance": true, "DistanceNd": true, "Div": true, "Dot": true, "EQ": true,
	"Escape": true, "ExtendsLeft": true, "ExtendsRight": true, "GT": true,
	"GTE": true, "Glob": true, "ILike": true, "IntDiv": true, "Is": true,
	"JSONArrayContains": true, "JSONBContains": true, "JSONBContainsAllTopKeys": true,
	"JSONBContainsAnyTopKeys": true, "JSONBContainsTopKey": true, "JSONBDeleteAtPath": true,
	"JSONBExtract": true, "JSONBExtractScalar": true, "JSONBPathExists": true,
	"JSONExtract": true, "JSONExtractScalar": true, "Kwarg": true, "LT": true,
	"LTE": true, "Like": true, "Match": true, "Mod": true, "Mul": true, "NEQ": true,
	"NestedJSONSelect": true, "NullSafeEQ": true, "NullSafeNEQ": true, "Operator": true,
	"Or": true, "Overlaps": true, "Pow": true, "PropertyEQ": true, "RegexpFullMatch": true,
	"RegexpILike": true, "RegexpLike": true, "SimilarTo": true, "Sub": true, "Xor": true,
}

// buildMod is the reference's own `build_mod`: MOD(a, b) wraps whichever
// argument is itself a binary expression in Paren, matching the precedence
// `%` would give it -- MOD(a + 1, 7) reads the same as (a + 1) % 7 written
// directly.
func (p *parser) buildMod(args []*Expression) *Expression {
	this, expression := args[0], args[1]
	if binaryClasses[this.Class] {
		this = New("Paren", Arg{"this", this})
	}
	if binaryClasses[expression.Class] {
		expression = New("Paren", Arg{"this", expression})
	}
	return New("Mod", Arg{"this", this}, Arg{"expression", expression})
}

func (p *parser) buildDateName(args []*Expression) (*Expression, error) {
	if len(args) != 2 {
		return nil, p.unsupported("function DATENAME with this many arguments")
	}
	part, this := args[0], args[1]
	spelled := formatTime(strings.ToLower(part.Name()), p.tables.FullFormatTimeMapping)
	to := New("DataType", Arg{"this", DataTypeKind("DATETIME2")})
	cast := New("Cast", Arg{"this", this}, Arg{"to", to})
	// A cast the port BUILDS carries its type already, as every cast the
	// reference builds does; see wrapStringArgument.
	cast.Type = to
	return New("TimeToStr",
		Arg{"this", cast},
		Arg{"format", New("Literal", Arg{"this", spelled}, Arg{"is_string", true})}), nil
}

// buildVarMap is Databricks' (standing in for the Hive/Spark family it
// shares this builder with) bare MAP(...): the reference's build_var_map
// takes positional arguments in pairs, an even-indexed one a key and the
// odd-indexed one after it the matching value, and wraps each side in its
// own Array -- VarMap(keys=Array([...]), values=Array([...])). A single `*`
// argument builds a StarMap over it instead. An odd argument count crashes
// the reference outright (IndexError), which the port turns into a refusal
// rather than reproducing the crash.
func (p *parser) buildVarMap(args []*Expression) (*Expression, error) {
	if len(args) == 1 && isStarProjection(args[0]) {
		return New("StarMap", Arg{"this", args[0]}), nil
	}
	if len(args)%2 != 0 {
		return nil, p.unsupported("MAP with an odd number of arguments")
	}
	keys := make([]*Expression, 0, len(args)/2)
	values := make([]*Expression, 0, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		keys = append(keys, args[i])
		values = append(values, args[i+1])
	}
	return New("VarMap",
		Arg{"keys", New("Array", Arg{"expressions", keys})},
		Arg{"values", New("Array", Arg{"expressions", values})}), nil
}

func argAt(args []*Expression, i int) *Expression {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// fromArgList places a call's arguments onto a class's own argument keys, in
// order, the way the reference's Func.from_arg_list does: the zip stops at
// whichever runs out first, and an argument that is not there at all leaves
// its key absent rather than present and empty.
//
// The reference's variadic tail is not handled -- no hand-written parser here
// places arguments onto a class that has one, and the probed signatures cover
// every call that does.
func fromArgList(class string, args []*Expression) *Expression {
	node := New(class)
	for i, key := range classArgKeys[class] {
		if i >= len(args) {
			break
		}
		node.Set(key, args[i])
	}
	return node
}

// wrapStringArgument rewrites a string argument the way the builder that
// receives it would. Only two shapes are recorded, and both are read off the
// reference rather than named here: a CAST to a type it chose, and the
// reference's own `to_interval` -- which is `INTERVAL <text>` parsed in the
// NEUTRAL dialect, whatever dialect the call was written in.
func (p *parser) wrapStringArgument(arg *Expression, how string) (*Expression, error) {
	text, _ := arg.Args["this"].(string)
	if kind, ok := strings.CutPrefix(how, "cast:"); ok {
		// No `nested` arg: a type the reference BUILDS carries only its
		// name, where one that was WRITTEN records nested as well.
		to := New("DataType", Arg{"this", DataTypeKind(kind)})
		cast := New("Cast", Arg{"this", arg}, Arg{"to", to})
		// A cast the port BUILDS carries its type already, as every cast the
		// reference builds does. Leaving it to the annotator is a different
		// tree: the type is recorded on the node, and the differential
		// compares it.
		cast.Type = to
		return cast, nil
	}
	if how == "interval" {
		interval, err := ParseOne("INTERVAL "+text, "")
		if err != nil || interval == nil || interval.Class != "Interval" {
			return nil, p.unsupported("a step this port cannot read as an interval")
		}
		return interval, nil
	}
	return nil, p.unsupported("a string argument this port cannot rewrite")
}

// buildToCharOrTimeToStr is Dremio's own `to_char_is_numeric_handler`,
// itself wrapping the reference's shared `build_timetostr_or_tochar`:
// a call of exactly two arguments has its first argument's TYPE recorded
// on the node itself (the same way a built cast always carries one), and
// where that type is temporal the call builds TimeToStr instead of ToChar,
// its format run through the dialect's own forward TimeMapping the same
// way DATE_FORMAT's format argument already is. Everything else builds
// ToChar, marked `is_numeric` when its format is a string literal naming
// a `#` pattern.
func (p *parser) buildToCharOrTimeToStr(args []*Expression) (*Expression, error) {
	this := argAt(args, 0)
	if this == nil {
		return nil, p.unsupported("TO_CHAR with no arguments")
	}
	format := argAt(args, 1)
	if len(args) == 2 {
		// The reference's `annotate_types` walks and stamps the WHOLE
		// subtree, not just this node -- a Column argument's own Identifier
		// carries a recorded UNKNOWN too, which is what AnnotateFully (built
		// for exactly this) reproduces.
		AnnotateFully(this, p.dialect)
		if temporalTypes[DataTypeKind(typeKind(this.Type))] {
			text, _ := format.Args["this"].(string)
			spelled := text
			if isStringLiteral(format) {
				spelled = formatTime(text, p.tables.TimeMapping)
			}
			return New("TimeToStr",
				Arg{"this", this},
				Arg{"format", New("Literal", Arg{"this", spelled}, Arg{"is_string", true})}), nil
		}
	}
	node := New("ToChar", Arg{"this", this}, Arg{"format", format}, Arg{"nlsparam", argAt(args, 2)})
	if format != nil {
		if text, _ := format.Args["this"].(string); isStringLiteral(format) && strings.Contains(text, "#") {
			node.Set("is_numeric", true)
		}
	}
	return node, nil
}

// buildDremioCurrentDateUTC is Dremio's `CURRENT_DATE_UTC()`: not a plain
// call at all, but a fixed shape -- today's date read out of a timestamp
// pinned to UTC -- the reference's `_parse_current_date_utc` builds
// whatever the parentheses do or don't hold.
func buildDremioCurrentDateUTC() *Expression {
	to := New("DataType", Arg{"this", DataTypeKind("DATE")})
	cast := New("Cast",
		Arg{"this", New("AtTimeZone",
			Arg{"this", New("CurrentTimestamp")},
			Arg{"zone", New("Literal", Arg{"this", "UTC"}, Arg{"is_string", true})})},
		Arg{"to", to})
	cast.Type = to
	return cast
}

// buildDremioDateDeltaWithCastInterval is Dremio's own
// `build_date_delta_with_cast_interval`: DATE_ADD/DATE_SUB's second
// argument, when it is a CAST to an INTERVAL type, is not read as a cast
// at all -- the value being cast becomes the delta itself and the
// interval's own unit moves onto the call, dropping the cast and its type
// entirely. Any other second argument falls through to the generic
// builder untouched, which is why this returns nil rather than a refusal.
func buildDremioDateDeltaWithCastInterval(class string, args []*Expression) *Expression {
	if len(args) != 2 {
		return nil
	}
	dateArg, intervalArg := args[0], args[1]
	if intervalArg.Class != "Cast" {
		return nil
	}
	to, _ := intervalArg.Args["to"].(*Expression)
	if to == nil || to.Class != "DataType" {
		return nil
	}
	interval, _ := to.Args["this"].(*Expression)
	if interval == nil || interval.Class != "Interval" {
		return nil
	}
	inner, _ := intervalArg.Args["this"].(*Expression)
	unit, _ := interval.Args["unit"].(*Expression)
	return New(class, Arg{"this", dateArg}, Arg{"expression", inner}, Arg{"unit", unit})
}

// buildDremioDateType is Dremio's `DATETYPE(year, month, day)` where all
// three arguments are plain integer literals: the reference's own
// `datetype_handler` folds them straight into a zero-padded date STRING
// rather than building a Concat -- `DATETYPE(2024, 2, 2)` reads the same as
// `DATE '2024-02-02'`. Where any argument is not a bare integer Literal, the
// reference instead builds a CAST of a CONCAT chain, a shape no statement in
// the pinned corpus exercises; this returns nil there rather than guess it,
// the same way an unverified branch elsewhere in this port declines.
func buildDremioDateType(args []*Expression) *Expression {
	if len(args) != 3 {
		return nil
	}
	year, month, day := args[0], args[1], args[2]
	for _, arg := range []*Expression{year, month, day} {
		text, _ := arg.Args["this"].(string)
		if arg.Class != "Literal" || !isIntegerText(text) {
			return nil
		}
	}
	yv, _ := strconv.Atoi(year.Args["this"].(string))
	mv, _ := strconv.Atoi(month.Args["this"].(string))
	dv, _ := strconv.Atoi(day.Args["this"].(string))
	dateStr := fmt.Sprintf("%04d-%02d-%02d", yv, mv, dv)
	return New("Date", Arg{"this", New("Literal", Arg{"this", dateStr}, Arg{"is_string", true})})
}

// mysqlTimeSpecifiers is the reference's own `TIME_SPECIFIERS`: the format
// letters that name a TIME-of-day field rather than a date one, in MySQL's
// OWN spelling -- checked against the format as WRITTEN, before this port's
// generic forward-TimeMapping normalization runs on it (which is also why
// this dispatches before that normalization, not after).
var mysqlTimeSpecifiers = map[byte]bool{
	'f': true, 'H': true, 'h': true, 'I': true, 'i': true, 'k': true,
	'l': true, 'p': true, 'r': true, 'S': true, 's': true, 'T': true,
}

func mysqlHasTimeSpecifier(format string) bool {
	for i := 0; i+1 < len(format); i++ {
		if format[i] == '%' && mysqlTimeSpecifiers[format[i+1]] {
			return true
		}
	}
	return false
}

// buildMySQLStrToDate is the reference's own `_str_to_date`: STR_TO_DATE
// builds StrToTime instead of StrToDate when its format names a time-of-day
// field, read off the format as WRITTEN, not the normalized spelling this
// port would otherwise carry into the tree.
func (p *parser) buildMySQLStrToDate(args []*Expression) *Expression {
	this, format := args[0], args[1]
	class := "StrToDate"
	normalized := format
	if isStringLiteral(format) {
		text, _ := format.Args["this"].(string)
		if mysqlHasTimeSpecifier(text) {
			class = "StrToTime"
		}
		normalized = New("Literal",
			Arg{"this", formatTime(text, p.tables.TimeMapping)}, Arg{"is_string", true})
	}
	return New(class, Arg{"this", this}, Arg{"format", normalized})
}

// buildMySQLDateDeltaWithInterval is the reference's own
// `build_date_delta_with_interval`: DATE_ADD/DATE_SUB's second argument is
// read as a real INTERVAL, and the call unwraps it -- the quantity and unit
// move onto the call directly, and the interval node itself is discarded.
// Any other second argument returns nil, matching the reference's own
// refusal (it raises outright rather than building something; no statement
// in the pinned corpus exercises the non-INTERVAL shape).
func buildMySQLDateDeltaWithInterval(upper string, args []*Expression) *Expression {
	interval := args[1]
	if interval.Class != "Interval" {
		return nil
	}
	quantity, _ := interval.Args["this"].(*Expression)
	unit, _ := interval.Args["unit"].(*Expression)
	class := map[string]string{"DATE_ADD": "DateAdd", "DATE_SUB": "DateSub"}[upper]
	return New(class, Arg{"this", args[0]}, Arg{"expression", quantity}, Arg{"unit", unit})
}
