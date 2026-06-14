package extutil

import (
	"strings"
	"testing"
)

func TestCleanOneLine(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"a\nb", 0, "a b"},
		{"a\tb\rc", 0, "a b c"},
		{"x\x1b[31my", 0, "x[31my"}, // ESC dropped
		{"  lots   of   space ", 0, "lots of space"},
		{"abcdef", 3, "abc…"},
		{"plain", 0, "plain"},
	}
	for _, c := range cases {
		if got := CleanOneLine(c.in, c.max); got != c.want {
			t.Errorf("CleanOneLine(%q,%d)=%q want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestSafeSessionID(t *testing.T) {
	for _, ok := range []string{"d00754e5-75b1-4a66-9213-2a47f4b3e977", "abc_1.2", "X"} {
		if !SafeSessionID(ok) {
			t.Errorf("%q should be safe", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a..b", "with space"} {
		if SafeSessionID(bad) {
			t.Errorf("%q should be unsafe", bad)
		}
	}
}

func TestSessionFileName(t *testing.T) {
	uuid := "d00754e5-75b1-4a66-9213-2a47f4b3e977"
	if got := SessionFileName("notes", uuid); got != "notes-"+uuid+".json" {
		t.Errorf("uuid filename: %q", got)
	}
	if bad := SessionFileName("notes", "x/../../escape"); strings.ContainsAny(bad, `/\`) || strings.Contains(bad, "..") {
		t.Errorf("hostile id produced unsafe name: %q", bad)
	}
}
