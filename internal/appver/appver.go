// Package appver compares SwitchDesk release versions.
package appver

import (
	"strconv"
	"strings"
)

// Newer reports whether version a is newer than b (dotted numbers, an
// optional leading "v" is ignored).
func Newer(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := part(pa, i), part(pb, i)
		if x != y {
			return x > y
		}
	}
	return false
}

func part(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	digits := strings.TrimFunc(p[i], func(r rune) bool { return r < '0' || r > '9' })
	n, _ := strconv.Atoi(digits)
	return n
}
