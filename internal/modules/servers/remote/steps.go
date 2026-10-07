package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

const (
	defaultRunTimeout  = 10 * time.Minute
	defaultWaitTimeout = 2 * time.Minute
	composeTimeout     = 10 * time.Minute
)

// RunStep runs one of the template's own step kinds in dir: run, compose-up,
// compose-down, wait-http. (base-bootstrap and upload-files are job steps of
// their own.) Arguments are already rendered.
func RunStep(ctx context.Context, env Env, c Conn, dir string, st manifest.Step) error {
	switch st.Kind {
	case "run":
		cmd, _ := st.Args["run"].(string)
		timeout := durationArg(st.Args, "timeout", defaultRunTimeout)
		env.info("Running %s", cmd)
		_, err := env.run(ctx, c, "run "+firstWord(cmd), CmdRun(dir, cmd), RunOpts{Timeout: timeout, Stream: true})
		return err
	case "compose-up":
		if pull, _ := st.Args["pull"].(bool); pull {
			env.info("Pulling images")
			if _, err := env.run(ctx, c, "docker compose pull", CmdComposePull(dir), RunOpts{Timeout: composeTimeout, Stream: true}); err != nil {
				return err
			}
		}
		env.info("Starting the stack")
		_, err := env.run(ctx, c, "docker compose up", CmdComposeUp(dir), RunOpts{Timeout: composeTimeout, Stream: true})
		return err
	case "compose-down":
		volumes, _ := st.Args["volumes"].(bool)
		env.info("Stopping the stack")
		_, err := env.run(ctx, c, "docker compose down", CmdComposeDown(dir, volumes), RunOpts{Timeout: composeTimeout, Stream: true})
		return err
	case "wait-http":
		return waitHTTP(ctx, env, c, st)
	}
	return fmt.Errorf("unknown step kind %q", st.Kind)
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// durationArg reads a duration argument ("5m"); a missing or malformed one
// (the manifest check refuses those) is the default.
func durationArg(args map[string]any, key string, def time.Duration) time.Duration {
	if s, ok := args[key].(string); ok {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// statusArg is the expected HTTP status (YAML gives an int).
func statusArg(args map[string]any) int {
	switch v := args["status"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 200
}

// waitHTTP asks the URL on the server every few seconds until it answers with
// the status, or the timeout passes. With resolve set the request goes to that
// address whatever DNS says for the host.
func waitHTTP(ctx context.Context, env Env, c Conn, st manifest.Step) error {
	url, _ := st.Args["url"].(string)
	resolve, _ := st.Args["resolve"].(string)
	want := statusArg(st.Args)
	timeout := durationArg(st.Args, "timeout", defaultWaitTimeout)
	cmd, err := CmdHTTP(url, resolve)
	if err != nil {
		return err
	}
	env.info("Waiting for %s to answer %d (up to %s)", url, want, timeout)
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		out, err := env.run(ctx, c, "request "+url, cmd, RunOpts{})
		code := strings.TrimSpace(out)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			code = "no answer (" + err.Error() + ")"
		case code == strconv.Itoa(want):
			env.info("%s answered %d", url, want)
			return nil
		case code == "000":
			code = "no answer"
		}
		if code != last {
			env.info("%s: %s", url, code)
			last = code
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer %d within %s (last: %s)", url, want, timeout, last)
		}
		if err := env.sleep(ctx, env.poll()); err != nil {
			return err
		}
	}
}
