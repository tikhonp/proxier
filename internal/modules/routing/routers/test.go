package routers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"golang.org/x/crypto/ssh"
)

// Test states.
const (
	TestRunning = "running"
	TestPassed  = "passed"
	TestWarned  = "warned"
	TestFailed  = "failed"
	TestConfirm = "confirm"
)

// Check names (i18n keys).
const (
	CheckVersion   = "routers.check.version"
	CheckForwarder = "routers.check.forwarder"
	CheckEntries   = "routers.check.entries"
	CheckUpload    = "routers.check.upload"
)

// Stopped is a test the admin stopped at a fingerprint.
const Stopped = "Stopped: the fingerprint wasn't confirmed. Nothing was pinned."

// Test is one Test connection run.
type Test struct {
	ID          int64
	RouterID    int64 // 0: the add form
	JobID       int64
	Conn        Connection
	State       string // running, passed, warned, failed, confirm
	Checks      []CheckResult
	ConfirmHop  string // jump, router
	ConfirmAddr string
	ConfirmFP   string
	Version     string
	Board       string
	Error       string
	CreatedAt   time.Time
}

// Done reports whether the test has ended (confirm waits for the admin).
func (t Test) Done() bool { return t.State != TestRunning }

// CheckResult is one line of a test's checks.
type CheckResult struct {
	Name   string `json:"name"` // i18n key
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail"`
}

func testOf(r store.Test) Test {
	t := Test{
		ID: r.ID, RouterID: r.RouterID, JobID: r.JobID, State: r.State, ConfirmHop: r.ConfirmHop, ConfirmAddr: r.ConfirmAddress,
		ConfirmFP: r.ConfirmFP, Version: r.Version, Board: r.Board, Error: r.Error, CreatedAt: r.CreatedAt.Time,
	}
	_ = json.Unmarshal([]byte(r.Form), &t.Conn)
	_ = json.Unmarshal([]byte(r.Checks), &t.Checks)
	return t
}

type testPayload struct {
	TestID int64 `json:"test_id"`
}

// StartTest queues Test connection of c: of the add form (routerID 0) or of
// a saved router.
func (s *Service) StartTest(ctx context.Context, routerID int64, c Connection, actor string) (int64, error) {
	c = c.Normalize()
	if fe := c.Validate(); fe != nil {
		return 0, fe
	}
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		req := jobs.Request{Type: JobTest, CreatedBy: actor}
		if routerID != 0 {
			if _, err := store.GetRouter(ctx, tx, routerID); errors.Is(err, store.ErrNotFound) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			req.ResourceKey, req.Subject = resourceKey(routerID), Subject(routerID)
		}
		var err error
		if id, err = store.InsertTest(ctx, tx, routerID, jsonOf(c), s.now()); err != nil {
			return err
		}
		req.Payload = testPayload{TestID: id}
		e, err := s.d.Jobs.Enqueue(ctx, tx, req)
		if err != nil {
			return err
		}
		return store.SetTestJob(ctx, tx, id, e.ID)
	})
	if err == nil {
		s.d.Jobs.Kick()
	}
	return id, err
}

// Test reads a test.
func (s *Service) Test(ctx context.Context, id int64) (Test, error) {
	r, err := store.GetTest(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Test{}, ErrTestNotFound
	}
	return testOf(r), err
}

// LatestTest is a router's newest test; false when it has none.
func (s *Service) LatestTest(ctx context.Context, routerID int64) (Test, bool, error) {
	r, err := store.LatestTest(ctx, s.d.DB.R, routerID)
	if errors.Is(err, store.ErrNotFound) {
		return Test{}, false, nil
	}
	return testOf(r), err == nil, err
}

// Confirm answers a test waiting at a fingerprint. trust pins the key it saw
// and starts a new test of the same form; otherwise the test ends failed and
// nothing is pinned.
func (s *Service) Confirm(ctx context.Context, testID int64, trust bool, actor string) (int64, error) {
	t, err := s.Test(ctx, testID)
	if err != nil {
		return 0, err
	}
	if t.State != TestConfirm {
		return 0, ErrNotConfirming
	}
	row, err := store.GetTest(ctx, s.d.DB.R, testID)
	if err != nil {
		return 0, err
	}
	end := func(text string) error {
		return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			return store.FinishTest(ctx, tx, store.Test{ID: testID, State: TestFailed, Checks: "[]", Error: text, FinishedAt: s.now()})
		})
	}
	if !trust {
		return 0, end(Stopped)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(row.ConfirmKey))
	if err != nil {
		return 0, fmt.Errorf("routers: the key to confirm: %w", err)
	}
	subject := RouterSubject(t.RouterID)
	if t.ConfirmHop == "jump" {
		subject = JumpSubject(t.ConfirmAddr)
	}
	if err := s.d.SSH.Pin(ctx, t.ConfirmAddr, subject, key, actor); err != nil && !errors.Is(err, sshx.ErrHostKnown) {
		return 0, err
	}
	if err := end("Fingerprint of " + t.ConfirmAddr + " confirmed: tested again."); err != nil {
		return 0, err
	}
	return s.StartTest(ctx, t.RouterID, t.Conn, actor)
}

// stepTest connects, reads the check and tries an upload.
func (s *Service) stepTest(ctx context.Context, r *jobs.Run) error {
	var p testPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	t, err := s.Test(ctx, p.TestID)
	if err != nil {
		return jobs.Permanent(err)
	}
	log := r.Log()
	c := t.Conn
	finish := func(res store.Test) error {
		res.ID, res.FinishedAt = t.ID, s.now()
		return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			if res.State == TestConfirm {
				return store.TestNeedsConfirm(ctx, tx, res)
			}
			if err := store.FinishTest(ctx, tx, res); err != nil {
				return err
			}
			if t.RouterID == 0 || res.Version == "" {
				return nil
			}
			return s.testedRouter(ctx, tx, t.RouterID, res, r.Info().Actor())
		})
	}

	cl, err := s.d.SSH.Connect(ctx, c.Target(t.RouterID), log)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var unknown *sshx.UnknownHostError
		if errors.As(err, &unknown) {
			hop := "router"
			if c.JumpHost != "" && unknown.Address == c.JumpAddress() {
				hop = "jump"
			}
			log.Info("First contact with %s (%s): waiting for the admin to confirm the fingerprint.", unknown.Address, unknown.Fingerprint)
			return finish(store.Test{
				State: TestConfirm, ConfirmHop: hop, ConfirmAddress: unknown.Address, ConfirmFP: unknown.Fingerprint,
				ConfirmKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(unknown.Key))),
			})
		}
		pr := ProblemOf(c, err)
		log.Warn("%s", pr.Text)
		return finish(store.Test{State: TestFailed, Checks: "[]", Error: pr.Text})
	}
	defer func() { _ = cl.Close() }()

	res, err := cl.Run(ctx, routeros.CmdCheck(c.Names), sshx.RunOptions{Timeout: time.Minute})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return finish(store.Test{State: TestFailed, Checks: "[]", Error: "The router's check failed: " + err.Error() + "."})
	}
	chk, err := routeros.ParseCheck(res.Stdout)
	if err != nil {
		return finish(store.Test{State: TestFailed, Checks: "[]", Error: "The router's check printed something unexpected: " + err.Error() + "."})
	}
	state := TestPassed
	checks := []CheckResult{{Name: CheckVersion, OK: true, Detail: chk.Version + " · " + chk.Board}}
	switch {
	case chk.Forwarder == 0:
		state = TestWarned
		checks = append(checks, CheckResult{Name: CheckForwarder, Warn: true,
			Detail: "This router doesn't look set up by the router script: no DoH forwarder " + c.Names.Forwarder + "."})
	case chk.Pin == 0:
		state = TestWarned
		checks = append(checks, CheckResult{Name: CheckForwarder, Warn: true,
			Detail: "This router doesn't look set up by the router script: no mtvpn:doh pin in " + c.Names.List + "."})
	default:
		checks = append(checks, CheckResult{Name: CheckForwarder, OK: true, Detail: "ok"})
	}
	checks = append(checks, CheckResult{Name: CheckEntries, OK: true, Detail: strconv.Itoa(chk.Entries)})
	file := "proxier-test-" + strconv.FormatInt(t.ID, 10) + ".rsc"
	if err := cl.Put(ctx, file, []byte("# proxier test connection\n")); err != nil {
		state = TestFailed
		checks = append(checks, CheckResult{Name: CheckUpload, Detail: "The router refused the upload (" + err.Error() +
			"): Proxier's group needs the ftp policy."})
	} else {
		if _, err := cl.Run(ctx, routeros.CmdRemoveFile(file), sshx.RunOptions{Timeout: time.Minute}); err != nil {
			log.Warn("Removing %s: %v", file, err)
		}
		checks = append(checks, CheckResult{Name: CheckUpload, OK: true, Detail: "ok"})
	}
	log.Info("RouterOS %s on %s: %s", chk.Version, chk.Board, state)
	out := store.Test{State: state, Checks: jsonOf(checks), Version: chk.Version, Board: chk.Board}
	if state == TestFailed {
		out.Error = "The file upload (SFTP) failed."
	}
	return finish(out)
}

// testedRouter writes what a saved router's test read; the first pass of a
// router that never connected connects it and queues its initial sync.
func (s *Service) testedRouter(ctx context.Context, tx *sqlx.Tx, id int64, res store.Test, actor string) error {
	cur, err := store.GetRouter(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	now := s.now()
	if err := store.RouterSeen(ctx, tx, id, res.Version, res.Board, now); err != nil {
		return err
	}
	if res.State != TestPassed && res.State != TestWarned {
		return nil
	}
	if cur.State == StateAwaiting {
		// the admin confirmed both keys: a pass activates it as the probe would
		return s.activate(ctx, tx, id, res.Version, res.Board, actor)
	}
	if !cur.ConnectedAt.IsZero() || cur.State != StateActive {
		return nil
	}
	return s.connected(ctx, tx, id, res.Version, res.Board, now, actor, true)
}

// connected marks a router's first successful connection; with initial, it
// also queues the initial sync.
func (s *Service) connected(ctx context.Context, tx *sqlx.Tx, id int64, version, board string, at db.Time, actor string, initial bool) error {
	if err := store.RouterConnected(ctx, tx, id, at); err != nil {
		return err
	}
	if err := s.record(ctx, tx, "routing.router_connected", id, actor, map[string]any{"version": version, "board": board}); err != nil {
		return err
	}
	if !initial {
		return nil
	}
	_, err := s.enqueueSync(ctx, tx, id, syncPayload{Trigger: TriggerInitial}, 0, actor)
	return err
}

// testFailed ends a test whose job failed before it could say why.
func (s *Service) testFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, err error) error {
	return store.TestsOfJobFailed(ctx, tx, j.ID, "The test failed: "+err.Error(), s.now())
}

func (s *Service) testCancelled(ctx context.Context, tx *sqlx.Tx, j jobs.Info, _ string) error {
	return s.testFailed(ctx, tx, j, errors.New("cancelled"))
}
