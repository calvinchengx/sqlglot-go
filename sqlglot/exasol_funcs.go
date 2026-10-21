package sqlglot

// Two of Exasol's functions do not MAP their arguments to a node -- they
// CHOOSE a node from the TYPE of one of those arguments. That is why the probe
// records nothing for them: it asks what a builder produces, and these ask what
// the argument is before they produce anything
// (build_timetostr_or_tochar and build_trunc, sqlglot/dialects/dialect.py).

// toCharOrTimeToStr reads `TO_CHAR(value[, format])` as a TimeToStr when the
// value is temporal and a ToChar otherwise. The format is mapped to the %-form
// the tree keeps.
func (p *parser) toCharOrTimeToStr(args []*Expression) *Expression {
	this := argAt(args, 0)
	if len(args) == 2 && this != nil {
		annotateInPlace(this, p.dialect)
	}
	if len(args) == 2 && this != nil &&
		temporalTypeKinds[typeKind(this.Type)] {
		format := argAt(args, 1)
		if lit, ok := format.Args["this"].(string); ok && format.Class == "Literal" {
			format = New("Literal",
				Arg{"this", formatTime(lit, p.tables.TimeMapping)},
				Arg{"is_string", true})
		}
		return New("TimeToStr", Arg{"this", this}, Arg{"format", format})
	}
	return New("ToChar", Arg{"this", this}, Arg{"format", argAt(args, 1)})
}

// truncOrDateTrunc reads `TRUNC(value[, part])`. A temporal value with a part
// beside it -- or a STRING part, which is the shape `TRUNC(date, 'Q')` has --
// is a DateTrunc; a numeric value is a Trunc; anything else stays the call the
// reference gives up on. DateTrunc unabbreviates the unit itself as it is
// built, so `'Q'` reaches the tree as QUARTER.
func (p *parser) truncOrDateTrunc(args []*Expression) *Expression {
	this, second := argAt(args, 0), argAt(args, 1)
	if this == nil {
		return New("Anonymous", Arg{"this", "TRUNC"}, Arg{"expressions", args})
	}
	annotateInPlace(this, p.dialect)
	thisType := typeKind(this.Type)
	secondType := ""
	if second != nil {
		annotateInPlace(second, p.dialect)
		secondType = typeKind(second.Type)
	}
	if (temporalTypeKinds[thisType] && second != nil) || textTypeKinds[secondType] {
		return New("DateTrunc", Arg{"this", this}, Arg{"unit", second})
	}
	if integerTypeKinds[thisType] || realTypeKinds[thisType] ||
		integerTypeKinds[secondType] || realTypeKinds[secondType] {
		return New("Trunc", Arg{"this", this}, Arg{"decimals", second},
			Arg{"fractions_supported", false})
	}
	return New("Anonymous", Arg{"this", "TRUNC"}, Arg{"expressions", args})
}

// annotateInPlace stamps a type on an argument the way the reference's builder
// does. `build_trunc` and `build_timetostr_or_tochar` call `annotate_types` on
// the node they were handed, which MUTATES it -- and the differential compares
// the annotation, so a port that read the type without recording it agrees
// about the answer and disagrees about the tree.
func annotateInPlace(e *Expression, dialect string) {
	if e == nil || e.Type != nil {
		return
	}
	AnnotateFully(e, dialect)
	if e.Type == nil {
		e.Type = Annotate(e, dialect)
	}
}
