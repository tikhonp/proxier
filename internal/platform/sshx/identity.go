package sshx

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"golang.org/x/crypto/ssh"
)

const identityAAD = "ssh:identity"

// PersonalKeysField is the admin's own public keys, one authorized_keys line
// per line; Phase 1 installs them on every new server beside Proxier's.
const PersonalKeysField = "ssh.personal_keys"

// Section is the "ssh" settings section.
var Section = settings.Section{
	Name: "ssh", Module: "platform",
	Fields: []settings.Field{
		{Key: PersonalKeysField, Kind: settings.String, MaxLen: 16000, Validate: ValidatePersonalKeys},
	},
}

// ValidatePersonalKeys checks that every line is an authorized_keys entry.
// Blank lines and # comments are allowed; a bad line is named by number.
func ValidatePersonalKeys(text string) error {
	_, err := parseKeys(text)
	return err
}

func parseKeys(text string) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
			return nil, fmt.Errorf("line %d is not a public key", n)
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// PersonalKeys returns the admin's public keys, one line each.
func (s *SSH) PersonalKeys(ctx context.Context) ([]string, error) {
	text, err := s.st.Get(ctx, PersonalKeysField)
	if err != nil {
		return nil, err
	}
	return parseKeys(text)
}

type identityRow struct {
	PrivateKey  []byte `db:"private_key"`
	PublicKey   string `db:"public_key"`
	Fingerprint string `db:"fingerprint"`
}

// EnsureIdentity creates Proxier's key pair when there is none. Calling it
// again keeps the key.
func (s *SSH) EnsureIdentity(ctx context.Context, instanceName string) error {
	var n int
	if err := s.d.R.GetContext(ctx, &n, `SELECT COUNT(*) FROM ssh_identity`); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.replaceIdentity(ctx, instanceName, events.ActorSystem, false)
}

// Regenerate replaces the key pair. Every host that trusted the old key must
// be given the new one.
func (s *SSH) Regenerate(ctx context.Context, actor string) error {
	name, err := s.st.Get(ctx, "general.instance_name")
	if err != nil {
		return err
	}
	return s.replaceIdentity(ctx, name, actor, true)
}

func (s *SSH) replaceIdentity(ctx context.Context, instanceName, actor string, regenerated bool) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	comment := "proxier@" + strings.Join(strings.Fields(instanceName), "-")
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment
	fp := ssh.FingerprintSHA256(sshPub)
	sealed := s.v.Seal(pem.EncodeToMemory(block), identityAAD)

	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		// A concurrent first start must not replace the key it did not see.
		verb := `INSERT INTO ssh_identity (id, private_key, public_key, fingerprint, created_at) VALUES (1, ?, ?, ?, ?)
			ON CONFLICT (id) DO NOTHING`
		if regenerated {
			verb = `INSERT INTO ssh_identity (id, private_key, public_key, fingerprint, created_at) VALUES (1, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET private_key = excluded.private_key, public_key = excluded.public_key,
				fingerprint = excluded.fingerprint, created_at = excluded.created_at`
		}
		res, err := tx.ExecContext(ctx, verb, sealed, line, fp, s.now())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = s.ev.Record(ctx, tx, events.Event{
			Type: "ssh.key_generated", Subject: identitySubject, Actor: actor,
			Payload: map[string]any{"fingerprint": fp, "regenerated": regenerated},
		})
		return err
	})
}

// PublicKey is Proxier's public key as an authorized_keys line, and its
// fingerprint.
func (s *SSH) PublicKey(ctx context.Context) (line, fingerprint string, err error) {
	var r identityRow
	if err := s.d.R.GetContext(ctx, &r, `SELECT private_key, public_key, fingerprint FROM ssh_identity WHERE id = 1`); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNoIdentity
		}
		return "", "", err
	}
	return r.PublicKey, r.Fingerprint, nil
}

func (s *SSH) signer(ctx context.Context) (ssh.Signer, error) {
	var r identityRow
	if err := s.d.R.GetContext(ctx, &r, `SELECT private_key, public_key, fingerprint FROM ssh_identity WHERE id = 1`); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoIdentity
		}
		return nil, err
	}
	pemBytes, err := s.v.Open(r.PrivateKey, identityAAD)
	if err != nil {
		return nil, fmt.Errorf("sshx: open the SSH key: %w", err)
	}
	return ssh.ParsePrivateKey(pemBytes)
}
