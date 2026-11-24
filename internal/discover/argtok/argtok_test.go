package argtok_test

import (
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/discover/argtok"
)

func TestValidAccepts(t *testing.T) {
	ok := []string{
		"stdio", "-v", "-abc", "--tools", "--tools=all", "--tools=a,b", "--x=-1",
		"/data", "a,b", "read-only", "x.y_z", "é", "@scope/pkg@1.0", "a=b", "a:b", "%20",
		strings.Repeat("x", argtok.MaxLen),
	}
	for _, s := range ok {
		if !argtok.Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
}

func TestValidRefuses(t *testing.T) {
	bad := []string{
		"", "-", "--", "---x", "-=x", "--=x", "--tools=", "-f=x", "--a b",
		"a b", "a\tb", "a\nb", "a\rb", "a\x00b", "a\x1bb", "a\u00a0b", "a\u202eb", "a\u200bb", "\xff",
		"--tools=all;id", "$(id)", "a`id`", "a;b", "a&b", "a|b", "a<b", "a>b", "$HOME", `a\b`,
		"(a)", "'a'", `"a"`, "a*", "a?", "[a]", "{a}", "{{x}}", "!a", "#a", "~/x",
		strings.Repeat("x", argtok.MaxLen+1),
	}
	for _, s := range bad {
		if argtok.Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

func TestFlag(t *testing.T) {
	for _, s := range []string{"-v", "--mode", "--a_b-c", "-1"} {
		if !argtok.Flag(s) {
			t.Errorf("Flag(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"mode", "--mode=x", "-", "--", "---x", "--a b", "--a;b", ""} {
		if argtok.Flag(s) {
			t.Errorf("Flag(%q) = true, want false", s)
		}
	}
}
