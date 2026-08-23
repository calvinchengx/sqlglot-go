package harness

import "encoding/json"

func parseFloor(raw []byte, into *map[string]float64) error { return json.Unmarshal(raw, into) }
