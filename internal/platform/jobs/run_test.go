package jobs_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func TestPayloadSecrets(t *testing.T) {
	h := newH(t)
	var readBack string
	var gone bool
	reg(t, h.Sys, jobs.Type{Name: "test.provision", Queue: jobs.Provisioning, MaxAttempts: 1, Steps: []jobs.Step{
		{Name: "login", Run: func(ctx context.Context, r *jobs.Run) error {
			v, ok, _ := r.Secret("root_password")
			if !ok {
				t.Error("secret not readable by the step")
			}
			readBack = v
			return r.DeleteSecret(ctx, "root_password")
		}},
		{Name: "after", Run: func(ctx context.Context, r *jobs.Run) error {
			var blob []byte
			if err := h.DB.R.Get(&blob, `SELECT secrets FROM jobs WHERE id = ?`, r.Info().ID); err != nil {
				return err
			}
			if blob != nil {
				m, err := h.Sys.OpenSecretsForTest(r.Info().ID, blob)
				if err != nil {
					return err
				}
				_, has := m["root_password"]
				_, other := m["keep"]
				gone = !has && other
			}
			if _, ok, _ := r.Secret("root_password"); ok {
				t.Error("the deleted secret is still in the run")
			}
			return jobs.Permanent(context.Canceled) // keep the job for the retry check
		}},
	}})
	h.Start(h.Sys)
	e := enq(t, h, jobs.Request{Type: "test.provision", Secrets: map[string]string{"root_password": "hunter2", "keep": "k"}})

	var blob []byte
	if err := h.DB.R.Get(&blob, `SELECT secrets FROM jobs WHERE id = ?`, e.ID); err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 || contains([]string{string(blob)}, "hunter2") {
		t.Fatal("secrets not sealed")
	}
	if _, err := h.Vault.Open(blob, "job:999:payload"); err == nil {
		t.Fatal("secrets opened under the wrong AAD")
	}
	if _, err := h.Vault.Open(blob, "job:1:payload"); err != nil {
		t.Fatalf("secrets not sealed under the job's AAD: %v", err)
	}
	h.Drain()
	if readBack != "hunter2" || !gone {
		t.Fatalf("read %q, gone %v", readBack, gone)
	}

	// A retry re-seals what is left under the new job's AAD.
	id, err := h.Sys.Retry(bg, e.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.DB.R.Get(&blob, `SELECT secrets FROM jobs WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Vault.Open(blob, "job:1:payload"); err == nil {
		t.Fatal("the retry's secrets are still sealed for the old job")
	}
	if _, err := h.Vault.Open(blob, "job:2:payload"); err != nil {
		t.Fatalf("retry secrets: %v", err)
	}
}
