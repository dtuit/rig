// Package shellq quotes words for a POSIX shell so an argv survives the trip
// through a remote login shell unchanged (issue 10).
package shellq

import "strings"

// safe lists the bytes that never need quoting in a POSIX shell word.
const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./=:,@%+"

// Quote returns s as a single shell word: plain words unchanged, the empty
// string as a pair of single quotes, everything else single-quoted with each
// embedded single quote written as quote-backslash-quote-quote.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	plain := true
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(safe, rune(s[i])) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes each argument and joins them with spaces, reproducing the argv
// the caller typed when the result is parsed by a remote shell.
func Join(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}
