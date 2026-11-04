// Package shellwords splits a command line into words with a minimal,
// predictable subset of POSIX shell quoting. It never expands anything.
package shellwords

import "errors"

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
		r := rs[i]
		switch r {
		case ' ', '\t':
			if inWord {
				words = append(words, string(cur))
				cur, inWord = cur[:0], false
			}
		case '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("trailing backslash")
			}
			i++
			cur, inWord = append(cur, rs[i]), true
		case '\'':
			end := -1
			for j := i + 1; j < len(rs); j++ {
				if rs[j] == '\'' {
					end = j
					break
				}
			}
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur, inWord = append(cur, rs[i+1:end]...), true
			i = end
		case '"':
			closed := false
			for i++; i < len(rs); i++ {
				if rs[i] == '"' {
					closed = true
					break
				}
				if rs[i] == '\\' && i+1 < len(rs) {
					i++
				}
				cur = append(cur, rs[i])
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
			inWord = true
		default:
			cur, inWord = append(cur, r), true
		}
	}
	if inWord {
		words = append(words, string(cur))
	}
	return words, nil
}
