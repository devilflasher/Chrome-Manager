package lnk

import (
	"encoding/json"
)

// Utilities.

// StructToJSON converts a struct into the equivalent JSON string.
// String is indented if indent is true.
// Remember only exported fields can be seen by the json package.
func StructToJSON(v interface{}, indent bool) string {
	// TODO: Should we panic instead?
	js, _ := json.MarshalIndent(v, "", "  ")
	return string(js)
}

// reverse returns its argument string reversed rune-wise left to right.
// Taken from https://github.com/golang/example/blob/master/stringutil/reverse.go.
func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < len(r)/2; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
