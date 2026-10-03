package sqlglot

// Oracle keeps @, $, and # inside a name: a$x#b is one identifier, and
// table_name@dblink is one name with the link still attached.
func init() {
	dialectConfigs["oracle"].VarSingleTokens = set{
		"@": struct{}{},
		"$": struct{}{},
		"#": struct{}{},
	}
}
