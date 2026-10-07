package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// Thresholds are the limits of the self-check: a certificate with fewer days
// left than CertWarnDays still passes but is reported; a disk with less than
// DiskWarnPct free passes with Low set, and under DiskFailPct it fails.
type Thresholds struct {
	CertWarnDays int
	DiskWarnPct  float64
	DiskFailPct  float64
	// Now is the clock the certificate's end date is compared with.
	Now func() time.Time
}

// DefaultThresholds are the constants of the manifest docs: 14 days, 10 %, 2 %.
var DefaultThresholds = Thresholds{CertWarnDays: 14, DiskWarnPct: 10, DiskFailPct: 2}

// CheckResult is one check's outcome.
type CheckResult struct {
	Kind string
	Pass bool
	// Message says what failed, or a short fact when it passed.
	Message string
	// ExpiringDays is set by cert-expiry when the certificate is under the
	// warning limit; Low by disk-free under its warning limit; FreePct and
	// DaysLeft are measured values.
	ExpiringDays int
	Low          bool
	FreePct      float64
	DaysLeft     int
	HasDaysLeft  bool
}

// SelfCheck runs the template's checks on the server in one go. A check that
// fails is a result with Pass false; the error is only for a connection that
// cannot run commands at all. dir is the stack's directory: compose-running
// looks there, and a cert-expiry file or a run command is relative to it.
func SelfCheck(ctx context.Context, env Env, c Conn, dir string, checks []manifest.Check, th Thresholds) ([]CheckResult, error) {
	if th.Now == nil {
		th.Now = time.Now
	}
	var out []CheckResult
	for _, ch := range checks {
		r, err := runCheck(ctx, env, c, dir, ch, th)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// SelfCheckReport runs the checks, logs each result and returns one error
// that names every check that failed; nil when all pass.
func SelfCheckReport(ctx context.Context, env Env, c Conn, dir string, checks []manifest.Check, th Thresholds) error {
	results, err := SelfCheck(ctx, env, c, dir, checks, th)
	if err != nil {
		return err
	}
	var bad []string
	for _, res := range results {
		if res.Pass {
			env.info("Check %s passed: %s", res.Kind, res.Message)
		} else {
			if env.Log != nil {
				env.Log.Error("Check %s failed: %s", res.Kind, res.Message)
			}
			bad = append(bad, res.Kind+": "+res.Message)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("the self-check failed: %s", strings.Join(bad, "; "))
	}
	return nil
}

func runCheck(ctx context.Context, env Env, c Conn, dir string, ch manifest.Check, th Thresholds) (CheckResult, error) {
	r := CheckResult{Kind: ch.Kind}
	switch ch.Kind {
	case "compose-running":
		out, err := env.run(ctx, c, "docker compose ps", CmdComposePS(dir), RunOpts{})
		if err != nil {
			if isExit(err) {
				r.Message = err.Error()
				return r, nil
			}
			return r, err
		}
		r.Pass, r.Message = composeRunning(out)
	case "http-local":
		url, _ := ch.Args["url"].(string)
		want := statusArg(ch.Args)
		cmd, err := CmdHTTP(url, "127.0.0.1")
		if err != nil {
			r.Message = err.Error()
			return r, nil
		}
		out, err := env.run(ctx, c, "request "+url, cmd, RunOpts{})
		if err != nil && !isExit(err) {
			return r, err
		}
		code := strings.TrimSpace(out)
		if code == strconv.Itoa(want) {
			r.Pass, r.Message = true, url+" answered "+code
		} else {
			if code == "" || code == "000" {
				code = "no answer"
			}
			r.Message = fmt.Sprintf("%s answered %s, expected %d", url, code, want)
		}
	case "cert-expiry":
		file, _ := ch.Args["file"].(string)
		if !strings.HasPrefix(file, "/") {
			file = path.Join(dir, file)
		}
		res, err := c.Run(ctx, CmdCertExpiry(file), sshx.RunOptions{})
		if err != nil {
			return r, err
		}
		switch {
		case res.ExitCode == 3:
			r.Message = "the certificate " + file + " is missing"
		case res.ExitCode != 0:
			r.Message = "cannot read the certificate: " + TailLines(3, res.Stderr, res.Stdout)
		default:
			end, perr := parseEndDate(string(res.Stdout))
			if perr != nil {
				r.Message = perr.Error()
				break
			}
			left := end.Sub(th.Now())
			r.DaysLeft, r.HasDaysLeft = int(left.Hours()/24), true
			switch {
			case left <= 0:
				r.Message = "the certificate expired on " + end.Format("2006-01-02")
			case r.DaysLeft < th.CertWarnDays:
				r.Pass, r.ExpiringDays = true, r.DaysLeft
				r.Message = fmt.Sprintf("the certificate expires in %d days", r.DaysLeft)
			default:
				r.Pass, r.Message = true, fmt.Sprintf("the certificate is valid for %d more days", r.DaysLeft)
			}
		}
	case "disk-free":
		out, err := env.run(ctx, c, "df", CmdDisk(), RunOpts{})
		if err != nil {
			if isExit(err) {
				r.Message = err.Error()
				return r, nil
			}
			return r, err
		}
		free, perr := ParseDiskFree(out)
		if perr != nil {
			r.Message = perr.Error()
			break
		}
		r.FreePct = free
		switch {
		case free < th.DiskFailPct:
			r.Message = fmt.Sprintf("only %.0f%% of the disk is free", free)
		case free < th.DiskWarnPct:
			r.Pass, r.Low = true, true
			r.Message = fmt.Sprintf("%.0f%% of the disk is free", free)
		default:
			r.Pass, r.Message = true, fmt.Sprintf("%.0f%% of the disk is free", free)
		}
	case "run":
		cmd, _ := ch.Args["run"].(string)
		_, err := env.run(ctx, c, "run "+firstWord(cmd), CmdRun(dir, cmd), RunOpts{Timeout: durationArg(ch.Args, "timeout", defaultRunTimeout)})
		if err != nil {
			if isExit(err) {
				r.Message = err.Error()
				return r, nil
			}
			return r, err
		}
		r.Pass, r.Message = true, cmd+" exited 0"
	default:
		r.Message = "unknown check " + ch.Kind
	}
	return r, nil
}

// composeRunning reads `docker compose ps --all --format json`, in either of
// its shapes: one JSON array, or one object per line (depending on the Compose
// version). Every service must run, and be healthy when it has a health check.
func composeRunning(out string) (bool, string) {
	type item struct{ Service, Name, State, Health string }
	items, err := decodeComposeJSON[item](out)
	if err != nil {
		return false, "cannot read docker compose ps: " + err.Error()
	}
	if len(items) == 0 {
		return false, "no containers are running"
	}
	var bad []string
	for _, it := range items {
		name := it.Service
		if name == "" {
			name = it.Name
		}
		switch {
		case it.State != "running":
			bad = append(bad, name+": "+it.State)
		case it.Health != "" && it.Health != "healthy":
			bad = append(bad, name+": "+it.Health)
		}
	}
	if len(bad) > 0 {
		return false, strings.Join(bad, ", ")
	}
	return true, fmt.Sprintf("%d containers running", len(items))
}

// parseEndDate reads openssl's "notAfter=Jan  2 15:04:05 2006 GMT".
func parseEndDate(out string) (time.Time, error) {
	_, v, ok := strings.Cut(strings.TrimSpace(out), "notAfter=")
	if !ok {
		return time.Time{}, fmt.Errorf("cannot read the certificate's end date from %q", strings.TrimSpace(out))
	}
	t, err := time.Parse("Jan _2 15:04:05 2006 MST", strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot read the certificate's end date %q", strings.TrimSpace(v))
	}
	return t, nil
}

// ParseDiskFree reads `df -P /` and returns the free share of the file
// system, in percent: available over used plus available.
func ParseDiskFree(out string) (float64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("cannot read df output")
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, fmt.Errorf("cannot read df output")
	}
	used, err1 := strconv.ParseFloat(f[2], 64)
	avail, err2 := strconv.ParseFloat(f[3], 64)
	if err1 != nil || err2 != nil || used+avail <= 0 {
		return 0, fmt.Errorf("cannot read df output")
	}
	return avail / (used + avail) * 100, nil
}

// decodeComposeJSON reads the output of `docker compose … --format json`, in
// either of its shapes: one JSON array, or one object per line (depending on
// the Compose version).
func decodeComposeJSON[T any](out string) ([]T, error) {
	var items []T
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "[") {
		return items, json.Unmarshal([]byte(out), &items)
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var it T
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}
