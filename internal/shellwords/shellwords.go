// Package shellwords splits a command line into words with a minimal,
// predictable subset of POSIX shell quoting. It never expands anything.
package shellwords

import (
	"errors"
	"slices"
)

// Split breaks s into words. Unquoted spaces and tabs separate words;
// single quotes group literally; double quotes group, and inside them a
// backslash escapes the next character; outside quotes a backslash escapes
// the next character. An unterminated quote or a trailing backslash is an
// error. Empty input yields an empty, non-nil slice.
func Split(s string) ([]string, error) {
	words := []string{}
	var cur []rune
	inWord := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		var err error
		switch rs[i] {
		case ' ', '\t':
			if inWord {
				words = append(words, string(cur))
				cur, inWord = cur[:0], false
			}
			continue
		case '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("trailing backslash")
			}
			i++
			cur = append(cur, rs[i])
		case '\'':
			cur, i, err = appendSingleQuoted(cur, rs, i)
		case '"':
			cur, i, err = appendDoubleQuoted(cur, rs, i)
		default:
			cur = append(cur, rs[i])
		}
		if err != nil {
			return nil, err
		}
		inWord = true
	}
	if inWord {
		words = append(words, string(cur))
	}
	return words, nil
}

// appendSingleQuoted appends the literal text of the single-quoted string
// opening at rs[open] to cur, and returns the index of its closing quote.
func appendSingleQuoted(cur, rs []rune, open int) ([]rune, int, error) {
	n := slices.Index(rs[open+1:], '\'')
	if n < 0 {
		return nil, open, errors.New("unterminated single quote")
	}
	end := open + 1 + n
	return append(cur, rs[open+1:end]...), end, nil
}

// appendDoubleQuoted appends the text of the double-quoted string opening
// at rs[open] to cur, a backslash taking the next character literally, and
// returns the index of its closing quote.
func appendDoubleQuoted(cur, rs []rune, open int) ([]rune, int, error) {
	for i := open + 1; i < len(rs); i++ {
		switch {
		case rs[i] == '"':
			return cur, i, nil
		case rs[i] == '\\' && i+1 < len(rs):
			i++
		}
		cur = append(cur, rs[i])
	}
	return nil, open, errors.New("unterminated double quote")
}
