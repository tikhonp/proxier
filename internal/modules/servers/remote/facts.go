package remote

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseOS turns /etc/os-release into "debian-12", "ubuntu-24.04": ID and
// VERSION_ID. A file with neither gives "".
func ParseOS(osRelease string) string {
	vals := map[string]string{}
	for _, line := range strings.Split(osRelease, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		vals[k] = strings.Trim(v, `"'`)
	}
	if vals["ID"] == "" {
		return ""
	}
	if vals["VERSION_ID"] == "" {
		return vals["ID"]
	}
	return vals["ID"] + "-" + vals["VERSION_ID"]
}

// ParseArch maps uname -m to the names templates use.
func ParseArch(uname string) string {
	switch a := strings.TrimSpace(uname); a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return a
	}
}

// OSAllowed reports whether found ("ubuntu-22.04") is among the template's
// supported systems. An empty list allows anything. An entry ending in "+"
// ("ubuntu-22.04+") allows that version and every later one of the same
// distribution.
func OSAllowed(found string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == found {
			return true
		}
		base, plus := strings.CutSuffix(a, "+")
		if !plus {
			continue
		}
		ad, av, ok1 := strings.Cut(base, "-")
		fd, fv, ok2 := strings.Cut(found, "-")
		if ok1 && ok2 && ad == fd && compareVersions(fv, av) >= 0 {
			return true
		}
	}
	return false
}

// compareVersions compares dotted numbers: 22.04 < 24.04, 9 < 12.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Listener is a process listening on a TCP port.
type Listener struct {
	Port    int
	Process string // "" when ss could not tell
	PID     int
}

var usersField = regexp.MustCompile(`users:\(\("([^"]*)",pid=(\d+)`)

// ParseListeners reads `ss -ltnpH`: the local address is the 4th column.
func ParseListeners(out string) []Listener {
	var res []Listener
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		i := strings.LastIndexByte(f[3], ':')
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(f[3][i+1:])
		if err != nil {
			continue
		}
		l := Listener{Port: port}
		if m := usersField.FindStringSubmatch(line); m != nil {
			l.Process = m[1]
			l.PID, _ = strconv.Atoi(m[2])
		}
		res = append(res, l)
	}
	return res
}

// Describe is "nginx (pid 812)".
func (l Listener) Describe() string {
	switch {
	case l.Process == "":
		return "an unknown process"
	case l.PID > 0:
		return fmt.Sprintf("%s (pid %d)", l.Process, l.PID)
	}
	return l.Process
}
