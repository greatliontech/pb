package env1

import (
	"strings"
	"unicode"
)

// Words splits an identifier into its words by word segmentation
// (check-rules.md): every character that is neither a letter nor a
// digit separates; a boundary falls at a lower-case letter followed
// by an upper-case one, within a run of upper-case letters before the
// last one when a lower-case letter follows it, and at a digit
// followed by an upper-case letter in a run that also holds a
// lower-case letter — an all-upper run is one word, so an
// UPPER_SNAKE name is its own words; none falls between a letter and
// a following digit or a digit and a following lower-case letter.
func Words(name string) []string {
	var words []string
	runes := []rune(name)
	start := -1
	mixed := false // the separator-delimited run holds a lower-case letter
	flush := func(end int) {
		if start >= 0 && end > start {
			words = append(words, string(runes[start:end]))
		}
		start = -1
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			mixed = false
			continue
		}
		if start < 0 && (i == 0 || !unicode.IsLetter(runes[i-1]) && !unicode.IsDigit(runes[i-1])) {
			mixed = false
			for _, x := range runes[i:] {
				if !unicode.IsLetter(x) && !unicode.IsDigit(x) {
					break
				}
				if unicode.IsLower(x) {
					mixed = true
					break
				}
			}
		}
		if start < 0 {
			start = i
			continue
		}
		prev := runes[i-1]
		boundary := (unicode.IsLower(prev) && unicode.IsUpper(r)) ||
			(unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1])) ||
			(mixed && unicode.IsDigit(prev) && unicode.IsUpper(r))
		if boundary {
			flush(i)
			start = i
		}
	}
	flush(len(runes))
	return words
}

// Acronym reports whether a word is an acronym: at least two letters,
// all upper-case.
func Acronym(word string) bool {
	letters := 0
	for _, r := range word {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			letters++
		}
	}
	return letters >= 2
}

// The case styles (REQ-env1-library).
const (
	StylePascal     = "pascal"
	StyleCamel      = "camel"
	StyleSnake      = "snake"
	StyleUpperSnake = "upper-snake"
)

// Case rebuilds an identifier from its words in a style, so that a
// name is in the style exactly when Case returns it unchanged; false
// for a style that is none of the four.
func Case(name, style string) (string, bool) {
	words := Words(name)
	switch style {
	case StyleSnake:
		return strings.ToLower(strings.Join(words, "_")), true
	case StyleUpperSnake:
		return strings.ToUpper(strings.Join(words, "_")), true
	case StylePascal, StyleCamel:
		var b strings.Builder
		for i, w := range words {
			switch {
			case i == 0 && style == StyleCamel:
				b.WriteString(strings.ToLower(w))
			case Acronym(w):
				b.WriteString(w)
			default:
				r := []rune(w)
				b.WriteString(string(unicode.ToUpper(r[0])) + strings.ToLower(string(r[1:])))
			}
		}
		return b.String(), true
	}
	return "", false
}
