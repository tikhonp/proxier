package routeros

import (
	"fmt"
	"regexp"
	"strings"
)

// Success is what a clean /import prints.
const Success = "Script file loaded and executed successfully"

// badLine are the patterns of a failed line: /import can exit 0 when a line
// failed. "not enough permissions" is a user whose group lacks write or ftp.
var badLine = regexp.MustCompile(`(?i)syntax error|failure|expected|no such item|does not match|bad command|invalid|not enough permissions`)

// noise is SSH's and the console's chatter that is never an error.
func noise(l string) bool {
	low := strings.ToLower(l)
	return strings.HasPrefix(low, "warning") || strings.HasPrefix(low, "** ") ||
		strings.Contains(low, "post-quantum") || strings.Contains(low, "store now, decrypt later") ||
		strings.Contains(low, "server may need to be upgraded")
}

// ScanImport is nil for a clean import, or an error quoting the first bad
// line. A non-zero exit fails too.
func ScanImport(exitCode int, out []byte) error {
	for _, l := range lines(out) {
		l = strings.TrimSpace(l)
		if noise(l) || strings.Contains(l, Success) {
			continue
		}
		if badLine.MatchString(l) {
			return fmt.Errorf("import: %s", l)
		}
	}
	if exitCode != 0 {
		first := ""
		for _, l := range lines(out) {
			if l = strings.TrimSpace(l); !noise(l) {
				first = ": " + l
				break
			}
		}
		return fmt.Errorf("import: exit code %d%s", exitCode, first)
	}
	return nil
}
