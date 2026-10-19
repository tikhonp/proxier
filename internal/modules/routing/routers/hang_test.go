package routers_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
)

// An /import the router runs in full and then never ends (no exit status, no
// channel close, Proxier's close ignored), as a real router behind a jump host
// once did. The sync must not hang on it.

// forgottenImport is a router behind a jump host whose first import is
// forgotten, following Main with openai. Run gives up on a session 100 ms
// after closing it.
func forgottenImport(t *testing.T) (*routingtest.Harness, *routerostest.Router, int64) {
	t.Helper()
	h := routingtest.New(t)
	h.App.SSH.CloseGrace = 100 * time.Millisecond
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Telepat", 0, routingtest.ViaJump())
	r.ForgetImport(1)
	return h, r, id
}

var openaiHash = routeros.EntriesHash(entries("openai.com", "chatgpt.com"))

func TestImportThatNeverEndsFailsTheAttemptAndTheRetryRecordsIt(t *testing.T) {
	h, r, id := forgottenImport(t)
	h.Mod.Routers.ImportTimeout = 300 * time.Millisecond
	job := syncNow(t, h, id)

	if st, _ := h.Job(job); st != "queued" {
		t.Fatalf("the attempt fails and waits for its retry: %s", st)
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "push" || truthy(ev[0].Payload["final"]) ||
		!strings.Contains(ev[0].Payload["error"].(string), "the import did not end within 300ms") {
		t.Fatalf("one failure at push, not final: %+v", ev)
	}
	if f := r.Files(); len(f) != 0 {
		t.Fatalf("the file is removed over a new connection: %v", f)
	}
	if applied(t, h, id)["openai"].Hash != openaiHash || !strings.Contains(jobLog(t, h, job), "openai: installed before the failure.") {
		t.Fatal("the import ran in full: the re-read records it")
	}
	if s := lastSync(t, h, id); s.State != "failed" || s.Step != "push" {
		t.Fatalf("row: %+v", s)
	}

	h.Advance(5 * time.Minute)
	h.Settle()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("the retry: %s %s", st, e)
	}
	if imp := r.Imports(); len(imp) != 1 {
		t.Fatalf("nothing left to push: %v", imp)
	}
	if rec := h.Events("routing.router_recovered"); len(rec) != 1 {
		t.Fatalf("recovered: %+v", rec)
	}
}

func TestCancelDuringImportThatNeverEndsCancelsTheJob(t *testing.T) {
	h, r, id := forgottenImport(t)
	h.StartJobs()
	job, err := h.Mod.Routers.SyncNow(bg, id, access.All, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the import", func() bool { return len(r.Imports()) == 1 })
	if err := h.App.Jobs.Cancel(bg, job, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()

	if st, _ := h.Job(job); st != "cancelled" {
		t.Fatalf("job: %s", st)
	}
	if s := lastSync(t, h, id); s.State != "cancelled" {
		t.Fatalf("row: %+v", s)
	}
	if ev := h.Events("routing.router_sync_failed"); len(ev) != 0 {
		t.Fatalf("a cancel is no failure: %+v", ev)
	}
	file := routeros.FileName(itoa(job), 1)
	if !slices.Contains(r.Files(), file) {
		t.Fatalf("the cancel leaves %s on the router: %v", file, r.Files())
	}
	// The router is free: the next sync removes the file and records what
	// the import installed.
	next := syncNow(t, h, id)
	if st, e := h.Job(next); st != "succeeded" {
		t.Fatalf("next sync: %s %s", st, e)
	}
	if f := r.Files(); len(f) != 0 {
		t.Fatalf("the next sync removes the cancelled job's file: %v", f)
	}
	if len(r.Imports()) != 1 || applied(t, h, id)["openai"].Hash != openaiHash {
		t.Fatalf("recorded, not pushed again: imports %v", r.Imports())
	}
}

func TestRestartDuringImportThatNeverEndsResumesAndRemovesTheFile(t *testing.T) {
	h, r, id := forgottenImport(t)
	h.StartJobs()
	job, err := h.Mod.Routers.SyncNow(bg, id, access.All, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the import", func() bool { return len(r.Imports()) == 1 })
	h.StopJobs()

	if st, _ := h.Job(job); st != "interrupted" {
		t.Fatalf("job: %s", st)
	}
	file := routeros.FileName(itoa(job), 1)
	if !slices.Contains(r.Files(), file) {
		t.Fatalf("the shutdown leaves %s on the router: %v", file, r.Files())
	}
	h.StartJobs()
	h.Settle()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("resumed job: %s %s", st, e)
	}
	if f := r.Files(); len(f) != 0 {
		t.Fatalf("the resume removes the leftover file: %v", f)
	}
	if len(r.Imports()) != 1 || applied(t, h, id)["openai"].Hash != openaiHash {
		t.Fatalf("recorded, not pushed again: imports %v", r.Imports())
	}
}

// The link to the jump host dies without a word right after the import ran:
// no exit status ever arrives, the jump host forgets the session, and every
// write through the link blocks (a router behind a Pi once hung like this for
// good, its import done). The keepalive gives up on the connection, and the
// attempt fails at push as for an import that never ends.
func TestJumpLinkThatDiesAfterTheImportFailsTheAttemptAndTheRetryRecordsIt(t *testing.T) {
	h := routingtest.New(t)
	h.App.SSH.CloseGrace = 100 * time.Millisecond
	h.App.SSH.KeepAlive = 20 * time.Millisecond
	links := sshxtest.NewLinks()
	h.App.SSH.Dial = links.Dial
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Air76", 0, routingtest.ViaJump())
	jump := h.JumpFake(id)
	r.OnImport(func(n int) {
		if n == 1 {
			links.Stall(jump.Addr)
		}
	})
	job := syncNow(t, h, id)

	if st, _ := h.Job(job); st != "queued" {
		t.Fatalf("the attempt fails and waits for its retry: %s", st)
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "push" || truthy(ev[0].Payload["final"]) {
		t.Fatalf("one failure at push, not final: %+v", ev)
	}
	if f := r.Files(); len(f) != 0 {
		t.Fatalf("the file is removed over a new connection: %v", f)
	}
	if applied(t, h, id)["openai"].Hash != openaiHash || !strings.Contains(jobLog(t, h, job), "openai: installed before the failure.") {
		t.Fatal("the import ran in full: the re-read records it")
	}

	h.Advance(5 * time.Minute)
	h.Settle()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("the retry: %s %s", st, e)
	}
	if imp := r.Imports(); len(imp) != 1 {
		t.Fatalf("nothing left to push: %v", imp)
	}
}

// The link to the jump host dies the same way while the file is uploading:
// nothing was imported, the attempt fails at push saying the connection was
// lost (not that the group lacks ftp), and the retry pushes the file.
func TestJumpLinkThatDiesDuringTheUploadFailsTheAttemptAndTheRetryPushes(t *testing.T) {
	h := routingtest.New(t)
	h.App.SSH.CloseGrace = 100 * time.Millisecond
	h.App.SSH.KeepAlive = 20 * time.Millisecond
	links := sshxtest.NewLinks()
	h.App.SSH.Dial = links.Dial
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Air76", 0, routingtest.ViaJump())
	jump := h.JumpFake(id)
	var once sync.Once
	r.Server.OnUpload(func(string) { once.Do(func() { links.Stall(jump.Addr) }) })
	job := syncNow(t, h, id)

	if st, _ := h.Job(job); st != "queued" {
		t.Fatalf("the attempt fails and waits for its retry: %s", st)
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "push" ||
		!strings.Contains(ev[0].Payload["error"].(string), "the connection to the router was lost") {
		t.Fatalf("one failure at push, the connection lost: %+v", ev)
	}
	if imp := r.Imports(); len(imp) != 0 {
		t.Fatalf("nothing was imported: %v", imp)
	}

	h.Advance(5 * time.Minute)
	h.Settle()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("the retry: %s %s", st, e)
	}
	if imp := r.Imports(); len(imp) != 1 || applied(t, h, id)["openai"].Hash != openaiHash {
		t.Fatalf("the retry pushes the file: %v", imp)
	}
	if f := r.Files(); len(f) != 0 {
		t.Fatalf("no file is left: %v", f)
	}
}

// The /import runs in full but its session ends without an exit status, as
// every RouterOS 7.24.5 command still running did when a keepalive reached the
// router and broke the connection. Behind a jump host the connection can't be trusted
// after that (a sync once opened its next session on it and waited for good):
// Proxier connects again to delete the file and re-read.
func TestImportWhoseSessionIsLostConnectsAgainAndTheRetryRecordsIt(t *testing.T) {
	h := routingtest.New(t)
	h.App.SSH.CloseGrace = 100 * time.Millisecond
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Telepat", 0, routingtest.ViaJump())
	r.LoseImport(1)
	job := syncNow(t, h, id)

	if st, _ := h.Job(job); st != "queued" {
		t.Fatalf("the attempt fails and waits for its retry: %s", st)
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "push" || truthy(ev[0].Payload["final"]) ||
		!strings.Contains(ev[0].Payload["error"].(string), "without exit status") {
		t.Fatalf("one failure at push, not final: %+v", ev)
	}
	log := jobLog(t, h, job)
	if !strings.Contains(log, "ended without an answer from the router; connecting again") {
		t.Fatalf("the lost connection is not reused: %s", log)
	}
	if f := r.Files(); len(f) != 0 {
		t.Fatalf("the file is removed over a new connection: %v", f)
	}
	if applied(t, h, id)["openai"].Hash != openaiHash || !strings.Contains(log, "openai: installed before the failure.") {
		t.Fatal("the import ran in full: the re-read records it")
	}

	h.Advance(5 * time.Minute)
	h.Settle()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("the retry: %s %s", st, e)
	}
	if imp := r.Imports(); len(imp) != 1 {
		t.Fatalf("nothing left to push: %v", imp)
	}
}
