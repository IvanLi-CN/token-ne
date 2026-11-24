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
		strings.Repeat("x", MaxPatternBytes),
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
		{strings.Repeat("x", MaxPatternBytes+1), "at most 256 bytes"},
		{strings.Repeat("é", 129), "at most 256 bytes"},
		{"gpt-[45]", "character classes"},
		{"a]", "character classes"},
		{"{a,b}", "character classes"},
		{`a\b`, "character classes"},
		{"a\nb", "control character"},
		{"a\x00b", "control character"},
		{"a\x7fb", "control character"},
		{"模型*", "non-ASCII"},
		{"café", "non-ASCII"},
		{"a\xffb", "non-ASCII"},
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

// TestMatchUnicodeLiteral pins the rune semantics: names may contain any UTF-8,
// '?' consumes exactly one rune rather than one byte, and a non-ASCII rune in
// the pattern is an ordinary literal when it bypasses Validate (Validate rejects
// non-ASCII patterns; see TestValidate).
func TestMatchUnicodeLiteral(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"model-*", "model-模型", true},
		{"?", "模", true},
		{"??", "模型", true},
		{"?*", "模型", true},
		{"a?c", "aéc", true},
		{"caf*", "café", true},
		{"é*", "éclair", true},
		{"é", "e", false},
		{"e", "é", false},
		{"모델*", "모델-1", true},
	} {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestOverlaps(t *testing.T) {
	for _, tc := range []struct {
		first, second string
		want          bool
	}{
		{"a*", "ab*", true},
		{"a?", "ab", true},
		{"a*", "b*", false},
		{"*-x", "a*", true},
		{"a?c", "a*c", true},
		{"a", "ab", false},
		{"*", "gpt-4o", true},
		{"*", "*", true},
		{"**", "*", true},
		{"gpt-4o", "gpt-4o", true},
		{"gpt-4o", "gpt-4o-mini", false},
		{"gpt-*", "gpt-4o*", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a*b", "b*a", false},
		{"*a", "a*", true},
		{"?a", "a?", true},
		{"a?", "?b", true},
		{"??", "a", false},
		{"a*", "a", true},
		{"a", "*", true},
	} {
		got, err := Overlaps(tc.first, tc.second)
		if err != nil {
			t.Fatalf("Overlaps(%q, %q) = %v", tc.first, tc.second, err)
		}
		if got != tc.want {
			t.Errorf("Overlaps(%q, %q) = %v, want %v", tc.first, tc.second, got, tc.want)
		}
		// Intersection emptiness is symmetric; a direction-dependent bug would
		// make some same-priority rule pairs depend on save order.
		if swapped, err := Overlaps(tc.second, tc.first); err != nil || swapped != tc.want {
			t.Errorf("Overlaps(%q, %q) = %v, %v; want %v, nil", tc.second, tc.first, swapped, err, tc.want)
		}
	}
	for _, tc := range []struct {
		first, second, want string
	}{
		{"", "*", "first glob pattern"},
		{"*", "a[", "second glob pattern"},
		{"a{", "b}", "first glob pattern"},
		{"*", "模型", "second glob pattern"},
		{strings.Repeat("x", MaxPatternBytes+1), "*", "first glob pattern"},
	} {
		overlaps, err := Overlaps(tc.first, tc.second)
		if err == nil {
			t.Fatalf("Overlaps(%q, %q) = %v, want an error", tc.first, tc.second, overlaps)
		}
		if overlaps {
			t.Fatalf("Overlaps(%q, %q) reported an overlap alongside %v", tc.first, tc.second, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Overlaps(%q, %q) = %q, want it to name %q", tc.first, tc.second, err, tc.want)
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
