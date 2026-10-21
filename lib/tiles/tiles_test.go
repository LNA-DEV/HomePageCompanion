package tiles

import "testing"

func TestParseZXY(t *testing.T) {
	cases := []struct {
		z, x, y string
		ok      bool
	}{
		{"0", "0", "0.mvt", true}, {"15", "32767", "32767.mvt", true},
		{"15", "32768", "0.mvt", false}, {"-1", "0", "0.mvt", false},
		{"3", "1", "1", false}, {"3", "a", "1.mvt", false}, {"99999999999", "0", "0.mvt", false},
	}
	for _, c := range cases {
		if _, _, _, ok := ParseZXY(c.z, c.x, c.y, "mvt", 15); ok != c.ok {
			t.Errorf("%s/%s/%s: ok=%v", c.z, c.x, c.y, ok)
		}
	}
}
