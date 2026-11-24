// Package modelglob is the single implementation of the restricted Glob
// language shared by upstream model policies, model-name routing and every
// validation layer that accepts those patterns.
//
// The language is deliberately tiny: a pattern is a sequence of literal
// characters plus two wildcards, '*' (zero or more characters) and '?'
// (exactly one character). Matching is full-string and case-sensitive. There is
// no regular expression, character class, brace expansion, escape sequence or
// similarity matching.
//
// Validate restricts patterns to printable ASCII literals: brackets, braces,
// backslash, control characters and non-ASCII bytes are rejected so that one
// pattern means the same thing in the control plane, in the core runtime and in
// config snapshots. This is stricter than the globPattern regexp in
// docs/schemas/config.schema.json, which only excludes brackets, braces,
// backslash and control characters.
//
// Matching is rune based, not byte based. Names are arbitrary UTF-8 and every
// non-wildcard rune in a name is a literal; '?' consumes exactly one rune and
// '*' consumes any run of runes. A caller that bypasses Validate can therefore
// still pass a pattern containing non-ASCII printable runes: Match treats them
// as literals that only match the same rune. Overlaps refuses such patterns
// because it validates both inputs.
package modelglob

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// MaxPatternBytes is the maximum accepted pattern length in bytes, mirroring the
// 256 character limit of model IDs and route patterns.
const MaxPatternBytes = 256

// Validate reports whether pattern is a legal pattern of the restricted Glob
// language. A pattern must be 1 to MaxPatternBytes bytes long, and every rune
// must be either a printable ASCII character (0x20-0x7e) or, for the two
// wildcards, '*' or '?'. Brackets, braces and backslash are rejected even
// though they are printable ASCII, as are control characters (including DEL)
// and any non-ASCII byte.
func Validate(pattern string) error {
	if pattern == "" {
		return errors.New("glob pattern must not be empty")
	}
	if len(pattern) > MaxPatternBytes {
		return fmt.Errorf("glob pattern must be at most %d bytes, got %d", MaxPatternBytes, len(pattern))
	}
	for offset, r := range pattern {
		switch {
		case r == '*' || r == '?':
		case r == '[' || r == ']' || r == '{' || r == '}' || r == '\\':
			return fmt.Errorf("glob pattern must not contain %q at byte %d: only * and ? are wildcards, character classes and escapes are not supported", r, offset)
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("glob pattern must not contain control character %#02x at byte %d", r, offset)
		case r > 0x7e:
			return fmt.Errorf("glob pattern must not contain non-ASCII character %q at byte %d: literals must be printable ASCII", r, offset)
		}
	}
	return nil
}

// Match reports whether name matches pattern in full and case-sensitively.
// '*' matches zero or more runes, '?' matches exactly one rune, and every other
// rune matches itself; a non-ASCII rune is therefore always a literal.
//
// Match does not validate pattern (callers that accept patterns from users call
// Validate first); it is total over arbitrary strings and treats a '*' or '?'
// anywhere in the pattern as a wildcard.
//
// The algorithm is greedy with a single backtrack point: each '*' records its
// pattern position and the name position it starts at, and each retry advances
// that name position by exactly one rune, so the name cursor never moves
// backwards. The worst case is O(len(pattern) * len(name)) time and O(1) extra
// space, with no recursion and no exponential backtracking.
func Match(pattern, name string) bool {
	var (
		patternAt, nameAt int
		starPatternAt     = -1
		starNameAt        int
	)
	for nameAt < len(name) {
		if patternAt < len(pattern) {
			patternRune, patternSize := utf8.DecodeRuneInString(pattern[patternAt:])
			if patternRune == '*' {
				starPatternAt = patternAt + patternSize
				starNameAt = nameAt
				patternAt = starPatternAt
				continue
			}
			if nameRune, nameSize := utf8.DecodeRuneInString(name[nameAt:]); patternRune == '?' || patternRune == nameRune {
				patternAt += patternSize
				nameAt += nameSize
				continue
			}
		}
		if starPatternAt < 0 {
			return false
		}
		_, nameSize := utf8.DecodeRuneInString(name[starNameAt:])
		starNameAt += nameSize
		nameAt = starNameAt
		patternAt = starPatternAt
	}
	for patternAt < len(pattern) && pattern[patternAt] == '*' {
		patternAt++
	}
	return patternAt == len(pattern)
}

// Overlaps reports whether some finite string matches both a and b, which is
// exactly the question "do two same-priority glob rules cover a common name?".
// Both patterns are validated; an invalid pattern returns a wrapped Validate
// error naming which argument failed.
//
// The decision is intersection emptiness of the two pattern automata. Each
// pattern is an NFA over states 0..len(pattern): a literal step consumes that
// rune, '?' consumes any rune, '*' consumes any rune while staying in place, and
// '*' may also be skipped for free. Overlaps explores the product automaton with
// breadth-first search, treating the free skips as ordinary edges and pairing
// only transitions that consume the same rune; two transitions are compatible
// when at least one accepts any rune, or when both require the same literal. The
// product accepts a string exactly when both patterns match it, so reaching the
// pair of accept states is equivalent to the existence of a common match.
// Because only literal/any compatibility is checked, the infinite alphabet
// collapses to the literals that occur in the two patterns.
//
// The work is O(len(a) * len(b)) time and memory: at most (len(a)+1)*(len(b)+1)
// product states, each with at most three outgoing edges. Patterns are capped at
// MaxPatternBytes by Validate, so the state space is bounded by 257*257.
func Overlaps(a, b string) (bool, error) {
	if err := Validate(a); err != nil {
		return false, fmt.Errorf("first glob pattern: %w", err)
	}
	if err := Validate(b); err != nil {
		return false, fmt.Errorf("second glob pattern: %w", err)
	}
	ar, br := []rune(a), []rune(b)
	width := len(br) + 1
	seen := make([]bool, (len(ar)+1)*width)
	queue := make([]int, 0, (len(ar)+1)*width)
	push := func(i, j int) {
		state := i*width + j
		if !seen[state] {
			seen[state] = true
			queue = append(queue, state)
		}
	}
	push(0, 0)
	for head := 0; head < len(queue); head++ {
		i, j := queue[head]/width, queue[head]%width
		if i == len(ar) && j == len(br) {
			return true, nil
		}
		// A '*' may match the empty string, which advances only that side.
		if i < len(ar) && ar[i] == '*' {
			push(i+1, j)
		}
		if j < len(br) && br[j] == '*' {
			push(i, j+1)
		}
		aRune, aAny, aNext, aOK := consume(ar, i)
		bRune, bAny, bNext, bOK := consume(br, j)
		if aOK && bOK && (aAny || bAny || aRune == bRune) {
			push(aNext, bNext)
		}
	}
	return false, nil
}

// consume reports the single rune-consuming transition out of state i. any is
// true when the token accepts any rune ('?' or '*'); otherwise lit is the exact
// rune the token requires. A state past the end has no transition.
func consume(pattern []rune, i int) (lit rune, any bool, next int, ok bool) {
	if i >= len(pattern) {
		return 0, false, 0, false
	}
	switch pattern[i] {
	case '*':
		return 0, true, i, true
	case '?':
		return 0, true, i + 1, true
	default:
		return pattern[i], false, i + 1, true
	}
}

// AdvisoryKey maps a name to the 64-bit key passed to
// pg_advisory_xact_lock so that writers reserving that name across models and
// route rules serialize on the same lock.
//
// The key is FNV-1a 64 over the exact UTF-8 bytes of name: every process, every
// release and every code path that reserves a name must call this function, so
// the same name always yields the same key without any stored state or shared
// counter. Callers must pass the stored name verbatim and decide any
// normalization before hashing; there is no case folding here because model IDs
// and patterns are case-sensitive.
//
// The hash is not cryptographic and 64-bit collisions are possible. A collision
// only makes two unrelated names take the same lock, which serializes them
// unnecessarily and never weakens the uniqueness check performed under the
// lock.
func AdvisoryKey(name string) int64 {
	const offset64 uint64 = 14695981039346656037
	const prime64 uint64 = 1099511628211
	hash := offset64
	for i := range len(name) {
		hash ^= uint64(name[i])
		hash *= prime64
	}
	return int64(hash)
}
