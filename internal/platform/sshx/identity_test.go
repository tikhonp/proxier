package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/settings"
	"golang.org/x/crypto/ssh"
)

func TestIdentityIsGeneratedOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	line1, fp1, err := e.ssh.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line1, "ssh-ed25519 ") || !strings.HasSuffix(line1, " proxier@Proxier") {
		t.Fatalf("public key line: %q", line1)
	}
	// A restart keeps the key.
	if err := e.ssh.EnsureIdentity(ctx, "Proxier"); err != nil {
		t.Fatal(err)
	}
	line2, fp2, _ := e.ssh.PublicKey(ctx)
	if line1 != line2 || fp1 != fp2 {
		t.Fatal("the key changed on the second start")
	}
	ev := e.events("ssh.key_generated")
	if len(ev) != 1 || ev[0].Payload["regenerated"] != false || ev[0].Payload["fingerprint"] != fp1 || ev[0].Actor != "system" {
		t.Fatalf("events: %+v", ev)
	}

	// The private key is sealed for this place only.
	var blob []byte
	if err := e.h.DB.R.Get(&blob, `SELECT private_key FROM ssh_identity`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "PRIVATE KEY") {
		t.Fatal("the private key is stored in plain text")
	}
	if _, err := e.h.Vault.Open(blob, "ssh:identity"); err != nil {
		t.Fatalf("open with its own place: %v", err)
	}
	if _, err := e.h.Vault.Open(blob, "setting:ssh.identity"); err == nil {
		t.Fatal("the key opens under another place")
	}
}

func TestRegenerateReplacesKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, before, _ := e.ssh.PublicKey(ctx)
	if err := e.ssh.Regenerate(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	_, after, _ := e.ssh.PublicKey(ctx)
	if before == after {
		t.Fatal("the key was not replaced")
	}
	if _, err := e.ssh.signer(ctx); err != nil {
		t.Fatalf("the new key does not open: %v", err)
	}
	ev := e.events("ssh.key_generated")
	if len(ev) != 2 || ev[0].Payload["regenerated"] != true || ev[0].Actor != "admin" || ev[0].Payload["fingerprint"] != after {
		t.Fatalf("events: %+v", ev)
	}
	// Only a regeneration notifies.
	typ, _ := e.h.Events.Lookup("ssh.key_generated")
	if !typ.Notify || !typ.NotifyIf(ev[0].Payload) || typ.NotifyIf(ev[1].Payload) {
		t.Fatal("key_generated must notify exactly when regenerated")
	}
}

func TestPersonalKeysAreValidated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, _ := ssh.NewPublicKey(pub)
	good := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " me@laptop"

	err := e.h.Settings.Set(ctx, "admin", "ssh", map[string]string{PersonalKeysField: good + "\n# a comment\n\nnot a key\n"})
	var fe settings.FieldErrors
	if !errors.As(err, &fe) || !strings.Contains(fe[PersonalKeysField], "line 4") {
		t.Fatalf("a bad line must be refused with its number, got %v", err)
	}
	if keys, _ := e.ssh.PersonalKeys(ctx); len(keys) != 0 {
		t.Fatal("a refused save stored keys")
	}

	if err := e.h.Settings.Set(ctx, "admin", "ssh", map[string]string{PersonalKeysField: good + "\n# a comment\n\n"}); err != nil {
		t.Fatal(err)
	}
	keys, err := e.ssh.PersonalKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0] != good {
		t.Fatalf("keys = %q, %v", keys, err)
	}
}
