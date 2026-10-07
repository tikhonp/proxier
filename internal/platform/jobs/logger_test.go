package jobs_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func runLogJob(t *testing.T, step func(r *jobs.Run), secrets map[string]string) (idOf int64, lines []string) {
	t.Helper()
	h := newH(t)
	reg(t, h.Sys, simple("test.log", func(_ context.Context, r *jobs.Run) error { step(r); return nil }))
	h.Start(h.Sys)
	e := enq(t, h, jobs.Request{Type: "test.log", Secrets: secrets})
	h.Drain()
	if st := h.State(e.ID); st != jobs.Succeeded {
		t.Fatalf("state %s", st)
	}
	return e.ID, logs(t, h, e.ID)
}

func TestLogRedactsSecrets(t *testing.T) {
	uuid := "3f6c1c0e-8c1d-4c57-9a0e-52d1f0b6a111"
	_, lines := runLogJob(t, func(r *jobs.Run) {
		r.Log().Redact(uuid)
		r.Log().Info("logging in with %s", "hunter2-root-password")
		r.Log().Info("client id %s", uuid)
		r.Log().Info("link /s/%s ok", "tok_ABCDEF123")
	}, map[string]string{"root_password": "hunter2-root-password", "token": "tok_ABCDEF123"})
	all := strings.Join(lines, "\n")
	for _, s := range []string{"hunter2", uuid, "tok_ABCDEF"} {
		if strings.Contains(all, s) {
			t.Fatalf("%q was stored: %s", s, all)
		}
	}
	if !contains(lines, "logging in with •••") || !contains(lines, "link /s/••• ok") {
		t.Fatalf("%q", lines)
	}
}

func TestRedactsEscapedForm(t *testing.T) {
	secret := "a b&c=d"
	_, lines := runLogJob(t, func(r *jobs.Run) {
		r.Log().Info("GET /x?p=%s", url.QueryEscape(secret))
		r.Log().Info("raw %s", secret)
	}, map[string]string{"s": secret})
	if !contains(lines, "GET /x?p=•••") || !contains(lines, "raw •••") {
		t.Fatalf("%q", lines)
	}
}

func TestLogKeepsHeadAndTailWithMarker(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.log", func(_ context.Context, r *jobs.Run) error {
		// With the step line, 15,000 lines in all.
		for i := 1; i <= 14999; i++ {
			r.Log().Info("line %d", i)
		}
		return nil
	}))
	h.Start(h.Sys)
	e := enq(t, h, jobs.Request{Type: "test.log"})
	h.Drain()
	j, _ := h.Sys.Job(bg, e.ID)
	if j.LogDropped != 5000 {
		t.Fatalf("dropped %d", j.LogDropped)
	}
	var kept []struct {
		ID    int64  `db:"id"`
		Level string `db:"level"`
		Text  string `db:"text"`
	}
	if err := h.DB.R.Select(&kept, `SELECT id, level, text FROM job_log_lines WHERE job_id = ? ORDER BY id`, e.ID); err != nil {
		t.Fatal(err)
	}
	// 15,000 lines: 10,000 kept, plus the marker.
	if len(kept) != 10001 {
		t.Fatalf("%d rows", len(kept))
	}
	if kept[0].Level != "step" || kept[1].Text != "line 1" || kept[4999].Text != "line 4999" {
		t.Fatalf("head: %q … %q", kept[0].Text, kept[4999].Text)
	}
	if kept[5000].Level != "marker" || kept[5000].Text != "… 5000 lines dropped …" {
		t.Fatalf("marker: %+v", kept[5000])
	}
	if kept[5001].Text != "line 10000" || kept[10000].Text != "line 14999" {
		t.Fatalf("tail: %q … %q", kept[5001].Text, kept[10000].Text)
	}
	for i := 1; i < len(kept); i++ {
		if kept[i].ID <= kept[i-1].ID {
			t.Fatal("line ids are not monotonic")
		}
	}
	// The viewer reads the same lines.
	tail, err := h.Sys.LogTail(bg, e.ID, 3)
	if err != nil || len(tail) != 3 || tail[2].Text != "line 14999" {
		t.Fatalf("%+v %v", tail, err)
	}
}

func TestLogShowsNoMarkerBeforeAnythingIsDropped(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.log", func(_ context.Context, r *jobs.Run) error {
		for i := 1; i <= 6000; i++ {
			r.Log().Info("line %d", i)
		}
		return nil
	}))
	h.Start(h.Sys)
	e := enq(t, h, jobs.Request{Type: "test.log"})
	h.Drain()
	lines, err := h.Sys.LogAfter(bg, e.ID, 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if l.Level == "marker" {
			t.Fatal("a marker without a drop")
		}
	}
}

func TestLogCutsLongLines(t *testing.T) {
	_, lines := runLogJob(t, func(r *jobs.Run) { r.Log().Info("%s", strings.Repeat("x", 5000)) }, nil)
	long := lines[len(lines)-1]
	if utf8.RuneCountInString(long) != 4000+len([]rune("… (truncated)")) || !strings.HasSuffix(long, "… (truncated)") {
		t.Fatalf("%d chars", utf8.RuneCountInString(long))
	}
}

func TestLogRedactsBeforeCutting(t *testing.T) {
	secret := "SECRETSECRETSECRETSECRET"
	_, lines := runLogJob(t, func(r *jobs.Run) {
		r.Log().Info("%s%s%s", strings.Repeat("a", 3990), secret, strings.Repeat("b", 100))
	}, map[string]string{"s": secret})
	last := lines[len(lines)-1]
	if strings.Contains(last, "SECRET") {
		t.Fatal("part of a secret across the cut was stored")
	}
	if !strings.Contains(last, "•••") {
		t.Fatalf("no redaction in %q", last[3980:])
	}
}

func TestWriterSplitsStreamedOutput(t *testing.T) {
	_, lines := runLogJob(t, func(r *jobs.Run) {
		w := r.Log().Writer("info")
		_, _ = fmt.Fprint(w, "one\ntw")
		_, _ = fmt.Fprint(w, "o\r\nthree")
		_ = w.Close()
	}, nil)
	n := len(lines)
	if lines[n-3] != "one" || lines[n-2] != "two" || lines[n-1] != "three" {
		t.Fatalf("%q", lines)
	}
}
