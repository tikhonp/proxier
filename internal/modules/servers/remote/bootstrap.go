package remote

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// Reconnect opens a new connection as the deploy user with Proxier's key.
type Reconnect func(ctx context.Context) (Conn, error)

// BaseBootstrap is the template's base-bootstrap step: SSH moved to sshPort
// and hardened, verified on a new connection there (reconnect's) and rolled
// back when that fails, the login banner, apt prerequisites, TCP BBR, Docker,
// and the firewall. Every sub-step is idempotent, because a restart re-runs
// the step from its start.
func BaseBootstrap(ctx context.Context, env Env, c Conn, reconnect Reconnect, sshPort int, ports []manifest.Port) error {
	if err := hardenSSH(ctx, env, c, reconnect, sshPort); err != nil {
		return err
	}
	if _, err := env.run(ctx, c, "silence the login banner", CmdHushlogin(), RunOpts{}); err != nil {
		return err
	}
	env.info("Installing prerequisites (curl, ca-certificates, openssl, ufw)")
	if _, err := env.run(ctx, c, "install prerequisites", CmdApt(), RunOpts{Timeout: 10 * time.Minute, Stream: true}); err != nil {
		return err
	}
	if err := ensureBBR(ctx, env, c); err != nil {
		return err
	}
	if err := ensureDocker(ctx, env, c); err != nil {
		return err
	}
	allow := []string{strconv.Itoa(sshPort) + "/tcp"}
	for _, p := range ports {
		allow = append(allow, fmt.Sprintf("%d/%s", p.Number, p.Proto))
	}
	env.info("Firewall: allowing %v", allow)
	_, err := env.run(ctx, c, "configure the firewall", CmdFirewall(allow), RunOpts{Stream: true})
	return err
}

// hardenSSH moves sshd to port and lets only the deploy user in, with a key.
// The old connection stays open while a new one on port proves key login
// still works; if it does not, the change is reverted through the old one.
func hardenSSH(ctx context.Context, env Env, c Conn, reconnect Reconnect, port int) error {
	env.info("Moving SSH to port %d, only %s with a key", port, DeployUser)
	if _, err := env.run(ctx, c, "unlock "+DeployUser, CmdUnlockUser(), RunOpts{}); err != nil {
		return err
	}
	if _, err := env.run(ctx, c, "allow the SSH port in ufw", CmdUFWAllow(strconv.Itoa(port)+"/tcp"), RunOpts{Stream: true}); err != nil {
		return err
	}
	if _, err := env.run(ctx, c, "write the sshd drop-in", CmdSSHDDropin(), RunOpts{Stdin: SSHDDropin(port)}); err != nil {
		return err
	}
	_, err := env.run(ctx, c, "reload sshd", CmdSSHDReload(), RunOpts{Stream: true})
	if err == nil {
		var fresh Conn
		if fresh, err = reconnect(ctx); err == nil {
			err = VerifyAccess(ctx, env, fresh)
			if cl, ok := fresh.(interface{ Close() error }); ok {
				_ = cl.Close()
			}
		}
	}
	if err == nil {
		return nil
	}
	env.warn("The SSH change broke key login (%v): reverting", err)
	if _, rerr := env.run(context.WithoutCancel(ctx), c, "revert the sshd change", CmdSSHDRollback(), RunOpts{Stream: true}); rerr != nil {
		return errors.Join(fmt.Errorf("SSH hardening broke key login and could not be reverted: %w", rerr), err)
	}
	return fmt.Errorf("SSH hardening broke key login and was reverted: %w", err)
}

// ensureBBR turns TCP BBR on. The server is the sender of what clients
// download through it, over long lossy paths, where BBR keeps the throughput
// CUBIC halves on every loss. It runs before Docker: a container's network
// namespace starts with the host's default congestion control. A kernel
// without BBR (OpenVZ, LXC, an odd build) only costs throughput, so it is a
// warning.
func ensureBBR(ctx context.Context, env Env, c Conn) error {
	out, err := env.run(ctx, c, "turn on TCP BBR", CmdBBR(), RunOpts{})
	if err != nil {
		return err
	}
	if cc := strings.TrimSpace(out); cc != "bbr" {
		env.warn("The kernel has no TCP BBR: TCP stays on %s", orUnknown(cc))
		return nil
	}
	env.info("TCP congestion control: bbr")
	return nil
}

func ensureDocker(ctx context.Context, env Env, c Conn) error {
	if _, err := env.run(ctx, c, "check Docker", CmdDockerCheck(), RunOpts{}); err == nil {
		env.info("Docker already installed, skipped")
		return nil
	} else if !isExit(err) {
		return err
	}
	env.info("Installing Docker")
	if _, err := env.run(ctx, c, "install Docker", CmdDockerInstall(), RunOpts{Timeout: 10 * time.Minute, Stream: true}); err != nil {
		return err
	}
	if _, err := env.run(ctx, c, "check Docker", CmdDockerCheck(), RunOpts{}); err != nil {
		return fmt.Errorf("docker is not usable after its installation: %w", err)
	}
	return nil
}

func isExit(err error) bool {
	var e *ExitError
	return errors.As(err, &e)
}
