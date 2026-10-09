package routers

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
)

// Problem is a connect error as the admin reads it: the hop that failed and
// one sentence for the band and the sync row.
type Problem struct {
	Hop  string // tailnet, jump, router
	Text string
}

// Untouched ends the text of a failure that changed nothing.
const Untouched = " Nothing was changed on the router."

// ProblemOf turns a connect error into its failing hop and sentence.
func ProblemOf(c Connection, err error) Problem {
	var he *sshx.HopError
	jump, addr := false, c.Address()
	if errors.As(err, &he) {
		jump, addr = he.Jump, he.Address
	}
	hop, who, whose := "router", "the router "+addr, "The router's"
	if jump {
		hop, who, whose = "jump", "the jump host "+addr, "The jump host's"
	}
	var (
		unknown *sshx.UnknownHostError
		changed *sshx.HostKeyChangedError
	)
	switch {
	case errors.Is(err, tailnet.ErrOff):
		return Problem{Hop: "tailnet", Text: "The tailnet is off (no PROXIER_TS_AUTHKEY)."}
	case errors.As(err, &unknown):
		return Problem{Hop: hop, Text: fmt.Sprintf("First contact with %s: confirm its fingerprint with Test connection.", unknown.Address)}
	case errors.As(err, &changed):
		return Problem{Hop: hop, Text: whose + " host key changed: accept the new key in Settings → SSH."}
	case errors.Is(err, sshx.ErrAuth):
		host, _, _ := net.SplitHostPort(addr)
		if jump {
			return Problem{Hop: hop, Text: fmt.Sprintf("The jump host %s refused Proxier's key: add it to authorized_keys there.", host)}
		}
		return Problem{Hop: hop, Text: fmt.Sprintf("The router %s refused Proxier's key: install it on the router.", host)}
	case errors.Is(err, sshx.ErrNoIdentity):
		return Problem{Hop: hop, Text: "Proxier has no SSH key yet."}
	}
	return Problem{Hop: hop, Text: fmt.Sprintf("Proxier can't reach %s: %s.", who, reason(err))}
}

// reason is the short cause of a network error: "connection refused",
// "no route to host", "i/o timeout".
func reason(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return "i/o timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "i/o timeout"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && strings.HasPrefix(s, "ssh: handshake failed") {
		s = s[i+2:]
	}
	return strings.TrimSuffix(s, ".")
}
