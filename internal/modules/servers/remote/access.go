package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// Facts are what preflight learned about the server.
type Facts struct {
	OS, Arch  string
	Listeners []Listener
}

// Preflight reads the operating system, the architecture and the listening
// TCP ports, and checks them against the template: the supported systems and
// architectures, and that the template's TCP ports are free. It runs as root
// before anything is changed.
func Preflight(ctx context.Context, env Env, c Conn, req manifest.Requires, ports []manifest.Port) (Facts, error) {
	var f Facts
	out, err := env.run(ctx, c, "read /etc/os-release", CmdOSRelease(), RunOpts{})
	if err != nil {
		return f, err
	}
	f.OS = ParseOS(out)
	if out, err = env.run(ctx, c, "read the architecture", CmdArch(), RunOpts{}); err != nil {
		return f, err
	}
	f.Arch = ParseArch(out)
	env.info("Operating system %s, architecture %s", orUnknown(f.OS), orUnknown(f.Arch))

	if !OSAllowed(f.OS, req.OS) {
		return f, fmt.Errorf("unsupported operating system %s: the template supports %s", orUnknown(f.OS), strings.Join(req.OS, ", "))
	}
	if len(req.Arch) > 0 && !contains(req.Arch, f.Arch) {
		return f, fmt.Errorf("unsupported architecture %s: the template supports %s", orUnknown(f.Arch), strings.Join(req.Arch, ", "))
	}

	if out, err = env.run(ctx, c, "list listening ports", CmdListeners(), RunOpts{}); err != nil {
		return f, err
	}
	f.Listeners = ParseListeners(out)
	for _, p := range ports {
		if p.Proto != "tcp" {
			continue
		}
		for _, l := range f.Listeners {
			if l.Port == p.Number {
				return f, fmt.Errorf("port %d is in use by %s", p.Number, l.Describe())
			}
		}
	}
	return f, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// InstallAccess makes the deploy user on a server, as root: the user (an
// existing one is reused), passwordless sudo through a sudoers file that
// visudo accepted, and keys appended where missing. keys are public key lines
// (Proxier's and the personal ones). The caller then opens a new connection
// as the deploy user and calls VerifyAccess; only then is the root password
// erased.
func InstallAccess(ctx context.Context, env Env, root Conn, keys []string) error {
	env.info("Creating the %s user", DeployUser)
	if _, err := env.run(ctx, root, "create the "+DeployUser+" user", CmdEnsureUser(), RunOpts{Stream: true}); err != nil {
		return err
	}
	env.info("Installing passwordless sudo for %s", DeployUser)
	if _, err := env.run(ctx, root, "install sudoers", CmdInstallSudoers(), RunOpts{Stdin: SudoersLine, Stream: true}); err != nil {
		return err
	}
	var lines []string
	seen := map[string]bool{}
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			seen[k] = true
			lines = append(lines, k)
		}
	}
	if len(lines) == 0 {
		return errors.New("no SSH key to install")
	}
	env.info("Installing %d SSH key(s) for %s", len(lines), DeployUser)
	_, err := env.run(ctx, root, "install SSH keys", CmdInstallKeys(), RunOpts{Stdin: strings.Join(lines, "\n") + "\n", Stream: true})
	return err
}

// VerifyAccess checks, on a connection opened as the deploy user with
// Proxier's key, that sudo works without a password.
func VerifyAccess(ctx context.Context, env Env, c Conn) error {
	if _, err := env.run(ctx, c, "sudo -n true", CmdSudoCheck(), RunOpts{}); err != nil {
		return err
	}
	env.info("Key login as %s works and sudo needs no password", DeployUser)
	return nil
}
