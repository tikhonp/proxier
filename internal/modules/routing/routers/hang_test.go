package routers_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
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
	job, err := h.Mod.Routers.SyncNow(bg, id, "admin")
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
	job, err := h.Mod.Routers.SyncNow(bg, id, "admin")
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
