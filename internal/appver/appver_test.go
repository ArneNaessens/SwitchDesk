package appver

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.4.0", "0.3.0", true}, {"0.10.0", "0.9.9", true}, {"0.3.0", "0.3.0", false},
		{"0.2.9", "0.3.0", false}, {"1.0", "0.9.9", true}, {"0.3.1", "0.3", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
