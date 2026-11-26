package modelglob

import (
	"hash/fnv"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, pattern := range []string{
		"*",
		"?",
		"**",
		"***?",
		"gpt-4o",
		"gpt-4o*",
		"a?c",
		"*-x",
		"a b",
		"model.name_v1:beta/2",
		`"'(),;!~@#%^&|+=<>`,
		"模型-*",
		"café",
		"🤖-?",
		strings.Repeat("x", MaxPatternLength),
		strings.Repeat("模", MaxPatternLength),
	} {
		if err := Validate(pattern); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", pattern, err)
		}
	}
	for _, tc := range []struct {
		pattern string
		want    string
	}{
		{"", "must not be empty"},
		{strings.Repeat("x", MaxPatternLength+1), "at most 256 characters"},
		{strings.Repeat("模", MaxPatternLength+1), "at most 256 characters"},
		{"gpt-[45]", "character classes"},
		{"a]", "character classes"},
		{"{a,b}", "character classes"},
		{`a\b`, "character classes"},
		{"a\nb", "control character"},
		{"a\x00b", "control character"},
		{"a\x7fb", "control character"},
		{"a\xffb", "valid UTF-8"},
	} {
		err := Validate(tc.pattern)
		if err == nil {
			t.Fatalf("Validate(%q) = nil, want an error", tc.pattern)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate(%q) = %q, want it to explain %q", tc.pattern, err, tc.want)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"gpt-4o", "gpt-4o", true},
		{"gpt-4o", "gpt-4o-mini", false},
		{"gpt-4o-mini", "gpt-4o", false},
		{"gpt-4o*", "gpt-4o", true},
		{"gpt-4o*", "gpt-4o-mini", true},
		{"gpt-*", "gpt-", true},
		{"*-x", "a-x", true},
		{"*-x", "a-x-y", false},
		{"*-x", "-x", true},
		{"*", "anything", true},
		{"**", "anything", true},
		{"**", "", true},
		{"*a*", "ba", true},
		{"*a*", "b", false},
		{"?", "", false},
		{"?", "a", true},
		{"?", "ab", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a?c", "abbc", false},
		{"a**b", "ab", true},
		{"a*b", "a**b", true},
		{"a*b", "ab", true},
		{"a*b", "a/c", false},
		{"gpt*", "GPT-4o", false},
		{"GPT*", "gpt-4o", false},
		{"", "", true},
		{"", "a", false},
	} {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// TestMatchDoubleStarEquivalent pins that "**" and "*" accept exactly the same
// names, and that "**" is not a special token of its own.
func TestMatchDoubleStarEquivalent(t *testing.T) {
	for _, name := range []string{"", "a", "gpt-4o", "模型", "*", "**"} {
		single, double := Match("*", name), Match("**", name)
		if single != double {
			t.Fatalf("Match(%q, %q) = %v but Match(%q, %q) = %v", "**", name, double, "*", name, single)
		}
		if !single {
			t.Fatalf("Match(%q, %q) = false, want true", "*", name)
		}
	}
}

// TestUnicodeLiterals pins the frozen-schema rule end to end: Chinese and emoji
// literals are legal pattern characters, matching is rune exact, and '?' consumes
// exactly one rune rather than one byte.
func TestUnicodeLiterals(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"model-*", "model-模型", true},
		{"模型-*", "模型-gpt-4o", true},
		{"模型-?", "模型-g", true},
		{"模型-?", "模型-gpt", false},
		{"?", "模", true},
		{"??", "模型", true},
		{"?*", "模型", true},
		{"🤖-*", "🤖-chat", true},
		{"?", "🤖", true},
		{"??", "🤖模", true},
		{"🤖", "🤖🤖", false},
		{"a?c", "aéc", true},
		{"caf*", "café", true},
		{"café", "café", true},
		{"café", "cafe", false},
		{"é*", "éclair", true},
		{"é", "e", false},
		{"e", "é", false},
		{"모델*", "모델-1", true},
	} {
		if err := Validate(tc.pattern); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", tc.pattern, err)
		}
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestAdvisoryKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int64
	}{
		{"gpt-4o", 1107710032109220332},
		{"model-*", 7834068510815985677},
		{"chat-fast", -2109763714842010856},
		{"", -3750763034362895579},
	} {
		if got := AdvisoryKey(tc.name); got != tc.want {
			t.Errorf("AdvisoryKey(%q) = %d, want %d", tc.name, got, tc.want)
		}
		if again := AdvisoryKey(tc.name); again != tc.want {
			t.Errorf("AdvisoryKey(%q) changed between calls: %d then %d", tc.name, tc.want, again)
		}
	}
	// Same name must always produce the same key, so the implementation is
	// pinned to standard FNV-1a 64 rather than an ad hoc hash.
	for _, name := range []string{"gpt-4o", "gpt-4o-mini", "模型", "*-x", "a b"} {
		h := fnv.New64a()
		_, _ = h.Write([]byte(name))
		if got, want := AdvisoryKey(name), int64(h.Sum64()); got != want {
			t.Errorf("AdvisoryKey(%q) = %d, want FNV-1a 64 %d", name, got, want)
		}
	}
	if AdvisoryKey("gpt-4o") == AdvisoryKey("gpt-4o-mini") {
		t.Fatal("distinct names produced the same advisory key")
	}
}
