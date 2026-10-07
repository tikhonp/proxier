package remote

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// Reconnect opens a new connection as the deploy user with Proxier's key.
type Reconnect func(ctx context.Context) (Conn, error)

// BaseBootstrap is the template's base-bootstrap step: SSH hardening verified
// on a new connection and rolled back when it breaks key login, the login
// banner, apt prerequisites, Docker, and the firewall. Every sub-step is
// idempotent, because a restart re-runs the step from its start.
func BaseBootstrap(ctx context.Context, env Env, c Conn, reconnect Reconnect, sshPort int, ports []manifest.Port) error {
	if err := hardenSSH(ctx, env, c, reconnect); err != nil {
		return err
	}
	if _, err := env.run(ctx, c, "silence the login banner", CmdHushlogin(), RunOpts{}); err != nil {
		return err
	}
	env.info("Installing prerequisites (curl, ca-certificates, openssl, ufw)")
	if _, err := env.run(ctx, c, "install prerequisites", CmdApt(), RunOpts{Timeout: 10 * time.Minute, Stream: true}); err != nil {
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

// hardenSSH turns password and root login off. The old connection stays open
// while a new one proves key login still works; if it does not, the change is
// reverted through the old one.
func hardenSSH(ctx context.Context, env Env, c Conn, reconnect Reconnect) error {
	env.info("Turning off password and root login over SSH")
	if _, err := env.run(ctx, c, "write the sshd drop-in", CmdSSHDDropin(), RunOpts{Stdin: SSHDDropin}); err != nil {
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
