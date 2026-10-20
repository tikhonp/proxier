package provision

import (
	"errors"
	"math/rand/v2"
	"net"
	"slices"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// A new server's SSH port is drawn from these: below Linux's ephemeral range
// (32768–60999), so no outgoing connection of the server holds it, and above
// the ports services usually take.
const (
	sshPortMin = 10000
	sshPortMax = 32767
)

func drawSSHPort(avoid []int) int {
	for {
		if p := sshPortMin + rand.IntN(sshPortMax-sshPortMin+1); !slices.Contains(avoid, p) {
			return p
		}
	}
}

// avoidPorts are the ports a new server's SSH port must not be: the one SSH
// listens on now, and every port the template uses.
func avoidPorts(setup int, ports []manifest.Port) []int {
	out := []int{setup}
	for _, p := range ports {
		out = append(out, p.Number)
	}
	return out
}

// unanswered reports whether a connection failed because nothing answers at
// the address (refused or timed out), not at the handshake or the login.
func unanswered(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
