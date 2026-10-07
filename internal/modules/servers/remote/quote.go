// Package remote is everything Proxier does on a server over SSH: the shell
// commands (each one built by a function here and nowhere else), the provisioning
// and deploy steps, the self-check. serverstest.VPS answers the same commands
// by parsing them with Parse, so changing a command here changes the fake too.
package remote

import (
	"errors"
	"strings"
)

// safe are the characters a word may hold unquoted.
func safe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("@%+=:,./_-", r)
}

// Quote makes s one shell word: bare when it holds only safe characters,
// otherwise in single quotes with every single quote written '\”.
func Quote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !safe(r) }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes each word and joins them with spaces.
func Join(words ...string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	return strings.Join(q, " ")
}

// Split is the inverse of Join for the commands this package builds: words
// separated by spaces, each bare or in single quotes (with '\” for a quote).
// It understands nothing else, which is the point: a command that needs more
// is not one of ours.
func Split(cmd string) ([]string, error) {
	var words []string
	i := 0
	for i < len(cmd) {
		if cmd[i] == ' ' {
			i++
			continue
		}
		var w strings.Builder
		for i < len(cmd) && cmd[i] != ' ' {
			switch {
			case cmd[i] == '\'':
				end := strings.IndexByte(cmd[i+1:], '\'')
				if end < 0 {
					return nil, errors.New("remote: unterminated quote")
				}
				w.WriteString(cmd[i+1 : i+1+end])
				i += end + 2
			case cmd[i] == '\\' && i+1 < len(cmd) && cmd[i+1] == '\'':
				w.WriteByte('\'')
				i += 2
			case safe(rune(cmd[i])):
				w.WriteByte(cmd[i])
				i++
			default:
				return nil, errors.New("remote: not a command of ours: " + cmd)
			}
		}
		words = append(words, w.String())
	}
	return words, nil
}
