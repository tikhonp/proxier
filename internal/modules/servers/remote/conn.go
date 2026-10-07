package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// Conn is what remote needs of an SSH connection; *sshx.Client is one.
type Conn interface {
	Run(ctx context.Context, cmd string, o sshx.RunOptions) (sshx.Result, error)
	Upload(ctx context.Context, path string, data []byte, mode fs.FileMode) error
}

var _ Conn = (*sshx.Client)(nil)

// Env is what steps share: the job's log and the pace of waits (tests make
// them fast).
type Env struct {
	Log *jobs.Logger
	// Poll is the pause between tries of wait-http (3 s).
	Poll time.Duration
	// Sleep waits d or until ctx ends; nil means a real wait.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (e Env) poll() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return 3 * time.Second
}

func (e Env) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e Env) info(format string, args ...any) {
	if e.Log != nil {
		e.Log.Info(format, args...)
	}
}

func (e Env) warn(format string, args ...any) {
	if e.Log != nil {
		e.Log.Warn(format, args...)
	}
}

// ExitError is a command that exited non-zero. Its message is the last lines
// of what it printed, which is what the page shows.
type ExitError struct {
	What string // "install access", "docker compose up"
	Code int
	Tail string
}

func (e *ExitError) Error() string {
	if e.Tail == "" {
		return fmt.Sprintf("%s failed (exit %d)", e.What, e.Code)
	}
	return fmt.Sprintf("%s failed (exit %d): %s", e.What, e.Code, e.Tail)
}

// TailLines keeps the last n non-empty lines of stdout and stderr.
func TailLines(n int, outs ...[]byte) string {
	var lines []string
	for _, o := range outs {
		for _, l := range strings.Split(strings.TrimSpace(string(o)), "\n") {
			if l = strings.TrimRight(l, " \r"); l != "" {
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// RunOpts tunes run.
type RunOpts struct {
	Timeout time.Duration
	Stdin   string
	Stream  bool // stream the output into the job log
}

// run executes cmd and returns its stdout. A non-zero exit is an *ExitError
// naming what; a timeout or a lost connection is the error of sshx.
func (e Env) run(ctx context.Context, c Conn, what, cmd string, o RunOpts) (string, error) {
	ro := sshx.RunOptions{Timeout: o.Timeout}
	if o.Stdin != "" {
		ro.Stdin = strings.NewReader(o.Stdin)
	}
	if o.Stream {
		ro.Log = e.Log
	}
	res, err := c.Run(ctx, cmd, ro)
	if err != nil {
		if errors.Is(err, sshx.ErrTimeout) {
			return "", fmt.Errorf("%s timed out", what)
		}
		return "", fmt.Errorf("%s: %w", what, err)
	}
	if res.ExitCode != 0 {
		return string(res.Stdout), &ExitError{What: what, Code: res.ExitCode, Tail: TailLines(20, res.Stderr, res.Stdout)}
	}
	return string(res.Stdout), nil
}
