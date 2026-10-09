package mtvpn

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// What a service list is refused for.
var ErrEmptyList = errors.New("the list has no entry")

// ListLineError: a line of a service list has whitespace inside.
type ListLineError struct{ Line int }

func (e *ListLineError) Error() string { return fmt.Sprintf("line %d has a space inside", e.Line) }

// ReadServiceList reads a hosted selector list as mtvpn's read_service_list:
// a "#" at the start of a line or after whitespace starts a comment, a
// leading "- " is dropped, a line ending in ":" is skipped.
func ReadServiceList(text string) ([]string, error) {
	var out []string
	for i, raw := range strings.Split(text, "\n") {
		line := raw
		for j := 0; j < len(line); j++ {
			if line[j] == '#' && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t') {
				line = line[:j]
				break
			}
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			line = strings.TrimSpace(line[2:])
		}
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		if strings.IndexFunc(line, unicode.IsSpace) >= 0 {
			return nil, &ListLineError{Line: i + 1}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, ErrEmptyList
	}
	return out, nil
}
