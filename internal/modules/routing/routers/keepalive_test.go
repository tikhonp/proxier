package routers_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

// RouterOS 7.24.5 answers a keepalive with SSH_MSG_UNIMPLEMENTED, which ended
// every command still running at the first one: once reading a router took
// over 30 s, every sync failed. An import that outlasts several keepalive
// periods must succeed on the first attempt, direct and behind a jump host.
func TestSyncThatOutlastsTheKeepAliveSucceeds(t *testing.T) {
	for name, opts := range map[string][]routingtest.RouterOption{
		"direct":             nil,
		"behind a jump host": {routingtest.ViaJump()},
	} {
		t.Run(name, func(t *testing.T) {
			h := routingtest.New(t)
			h.App.SSH.KeepAlive = 20 * time.Millisecond
			h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
			h.List("Main", h.Upstream("v2fly:openai"))
			r, id := h.Router("Penguin", 0, opts...)
			time.AfterFunc(100*time.Millisecond, r.Hold(1))
			job := syncNow(t, h, id)

			if st, e := h.Job(job); st != "succeeded" {
				t.Fatalf("the first attempt: %s %s", st, e)
			}
			if ev := h.Events("routing.router_sync_failed"); len(ev) != 0 {
				t.Fatalf("no failure: %+v", ev)
			}
			if imp := r.Imports(); len(imp) != 1 {
				t.Fatalf("one import: %v", imp)
			}
			if applied(t, h, id)["openai"].Hash != openaiHash {
				t.Fatal("openai is recorded as installed")
			}
		})
	}
}
