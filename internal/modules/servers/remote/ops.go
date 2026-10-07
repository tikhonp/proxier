package remote

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RestartStack restarts the stack's containers in place.
func RestartStack(ctx context.Context, env Env, c Conn, dir string) error {
	env.info("Restarting the stack")
	_, err := env.run(ctx, c, "docker compose restart", CmdComposeRestart(dir), RunOpts{Timeout: composeTimeout, Stream: true})
	return err
}

// PullImages pulls the newest images of the stack.
func PullImages(ctx context.Context, env Env, c Conn, dir string) error {
	env.info("Pulling images")
	_, err := env.run(ctx, c, "docker compose pull", CmdComposePull(dir), RunOpts{Timeout: composeTimeout, Stream: true})
	return err
}

// StartStack runs `docker compose up -d`: containers whose image or
// configuration changed are recreated, the others are left alone.
func StartStack(ctx context.Context, env Env, c Conn, dir string) error {
	env.info("Starting the stack")
	_, err := env.run(ctx, c, "docker compose up", CmdComposeUp(dir), RunOpts{Timeout: composeTimeout, Stream: true})
	return err
}

// Reboot asks the machine to restart. The connection drops while it does, so
// an error from a dropped session is not a failure; a refusal (non-zero exit
// before the drop) is.
func Reboot(ctx context.Context, env Env, c Conn) error {
	env.info("Rebooting")
	_, err := env.run(ctx, c, "reboot", CmdReboot(), RunOpts{Timeout: 30 * time.Second})
	if err == nil {
		return nil
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return err
	}
	// A dropped connection or a timeout is what a reboot looks like.
	env.info("The connection ended (%v), as expected", err)
	return nil
}

// BootID returns the id of the machine's current boot.
func BootID(ctx context.Context, env Env, c Conn) (string, error) {
	out, err := env.run(ctx, c, "read the boot id", CmdBootID(), RunOpts{Timeout: 30 * time.Second})
	return strings.TrimSpace(out), err
}

// ComposeServices lists the names of the stack's services, running or not.
func ComposeServices(ctx context.Context, env Env, c Conn, dir string) ([]string, error) {
	out, err := env.run(ctx, c, "docker compose ps", CmdComposePS(dir), RunOpts{})
	if err != nil {
		return nil, err
	}
	type item struct{ Service, Name string }
	items, err := decodeComposeJSON[item](out)
	if err != nil {
		return nil, fmt.Errorf("cannot read docker compose ps: %w", err)
	}
	var names []string
	seen := map[string]bool{}
	for _, it := range items {
		n := it.Service
		if n == "" {
			n = it.Name
		}
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Image is one container's image, as `docker compose images` reports it.
type Image struct {
	Container  string `json:"ContainerName"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	ID         string `json:"ID"`
}

// Name is the image as people write it: repository:tag.
func (i Image) Name() string {
	if i.Tag == "" {
		return i.Repository
	}
	return i.Repository + ":" + i.Tag
}

// ComposeImages returns the image of every container of the stack by
// container name.
func ComposeImages(ctx context.Context, env Env, c Conn, dir string) (map[string]Image, error) {
	out, err := env.run(ctx, c, "docker compose images", CmdComposeImages(dir), RunOpts{})
	if err != nil {
		return nil, err
	}
	items, err := decodeComposeJSON[Image](out)
	if err != nil {
		return nil, fmt.Errorf("cannot read docker compose images: %w", err)
	}
	m := make(map[string]Image, len(items))
	for _, it := range items {
		m[it.Container] = it
	}
	return m, nil
}

// ChangedImages names the images (repository:tag, sorted) whose ID differs
// between two listings, or that are new in after.
func ChangedImages(before, after map[string]Image) []string {
	seen := map[string]bool{}
	var out []string
	for k, a := range after {
		if b, ok := before[k]; ok && b.ID == a.ID {
			continue
		}
		if !seen[a.Name()] {
			seen[a.Name()] = true
			out = append(out, a.Name())
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// ServiceLogs streams the last 200 lines of one service into the job log.
func ServiceLogs(ctx context.Context, env Env, c Conn, dir, service string) error {
	env.info("Last 200 lines of %s", service)
	_, err := env.run(ctx, c, "docker compose logs "+service, CmdComposeLogs(dir, service), RunOpts{Timeout: 2 * time.Minute, Stream: true})
	return err
}
