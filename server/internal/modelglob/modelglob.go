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
// Validate accepts exactly the patterns of the frozen config schema
// (docs/schemas/config.schema.json#/$defs/globPattern): any valid UTF-8 text
// without '[' ']' '{' '}' '\' or the control characters U+0000-U+001F and
// U+007F, at most 256 code points. Every other character, including non-ASCII
// and non-Latin ones, is a literal, so one pattern means the same thing in the
// control plane, in the core runtime and in config snapshots.
//
// Matching is rune based, not byte based. Names are arbitrary UTF-8 and every
// non-wildcard rune in a name is a literal; '?' consumes exactly one rune and
// '*' consumes any run of runes. A pattern literal therefore matches only the
// same rune.
package modelglob

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// MaxPatternLength is the maximum accepted pattern length in Unicode code
// points, mirroring the 256 character limit of model IDs and route patterns.
const MaxPatternLength = 256

// Validate reports whether pattern is a legal pattern of the restricted Glob
// language. A pattern must be valid UTF-8, 1 to MaxPatternLength code points
// long, and free of '[' ']' '{' '}' '\' and the control characters U+0000-U+001F
// and U+007F. Every other character is a literal, including non-ASCII ones.
func Validate(pattern string) error {
	if pattern == "" {
		return errors.New("glob pattern must not be empty")
	}
	if !utf8.ValidString(pattern) {
		return errors.New("glob pattern must be valid UTF-8")
	}
	if n := utf8.RuneCountInString(pattern); n > MaxPatternLength {
		return fmt.Errorf("glob pattern must be at most %d characters, got %d", MaxPatternLength, n)
	}
	for offset, r := range pattern {
		switch {
		case r == '[' || r == ']' || r == '{' || r == '}' || r == '\\':
			return fmt.Errorf("glob pattern must not contain %q at byte %d: only * and ? are wildcards, character classes and escapes are not supported", r, offset)
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("glob pattern must not contain control character %#02x at byte %d", r, offset)
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
