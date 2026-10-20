package sshx

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"golang.org/x/crypto/ssh"
)

// KnownHost is a pinned host key. A key that changed waits as pending until
// the admin accepts it or forgets the host.
type KnownHost struct {
	ID                      int64
	Address, Subject        string
	KeyType, Fingerprint    string
	FirstSeenAt, AcceptedAt db.Time
	LastUsedAt              db.Time
	PendingFingerprint      string // "" when none
	PendingSeenAt           db.Time
}

type hostRow struct {
	ID                 int64   `db:"id"`
	Address            string  `db:"address"`
	Subject            string  `db:"subject"`
	KeyType            string  `db:"key_type"`
	PublicKey          []byte  `db:"public_key"`
	Fingerprint        string  `db:"fingerprint"`
	FirstSeenAt        db.Time `db:"first_seen_at"`
	AcceptedAt         db.Time `db:"accepted_at"`
	LastUsedAt         db.Time `db:"last_used_at"`
	PendingKeyType     string  `db:"pending_key_type"`
	PendingPublicKey   []byte  `db:"pending_public_key"`
	PendingFingerprint string  `db:"pending_fingerprint"`
	PendingSeenAt      db.Time `db:"pending_seen_at"`
}

const hostColumns = `id, address, subject, key_type, public_key, fingerprint, first_seen_at, accepted_at, last_used_at,
	COALESCE(pending_key_type, '') AS pending_key_type, pending_public_key,
	COALESCE(pending_fingerprint, '') AS pending_fingerprint, pending_seen_at`

func (r hostRow) host() KnownHost {
	return KnownHost{ID: r.ID, Address: r.Address, Subject: r.Subject, KeyType: r.KeyType, Fingerprint: r.Fingerprint,
		FirstSeenAt: r.FirstSeenAt, AcceptedAt: r.AcceptedAt, LastUsedAt: r.LastUsedAt,
		PendingFingerprint: r.PendingFingerprint, PendingSeenAt: r.PendingSeenAt}
}

// NormalizeAddress is the form pins are stored under: "host:port", the host
// lower-cased, the port 22 when absent.
func NormalizeAddress(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = strings.Trim(addr, "[]"), "22"
	}
	return net.JoinHostPort(strings.ToLower(host), port)
}

// KnownHosts lists the pinned hosts, those with a pending key first.
func (s *SSH) KnownHosts(ctx context.Context) ([]KnownHost, error) {
	var rows []hostRow
	err := s.d.R.SelectContext(ctx, &rows, `SELECT `+hostColumns+` FROM known_hosts
		ORDER BY pending_fingerprint IS NULL, address`)
	if err != nil {
		return nil, err
	}
	out := make([]KnownHost, len(rows))
	for i, r := range rows {
		out[i] = r.host()
	}
	return out, nil
}

func (s *SSH) known(ctx context.Context, address string) (*hostRow, error) {
	var r hostRow
	err := s.d.R.GetContext(ctx, &r, `SELECT `+hostColumns+` FROM known_hosts WHERE address = ?`, address)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// Pin trusts key for address. Pinning the same key again changes nothing;
// another key for a pinned address is ErrHostKnown (use Accept or Forget).
func (s *SSH) Pin(ctx context.Context, address, subject string, key ssh.PublicKey, actor string) error {
	address = NormalizeAddress(address)
	return s.d.Write(ctx, func(tx *sqlx.Tx) error { return s.pinTx(ctx, tx, address, subject, key, actor) })
}

func (s *SSH) pinTx(ctx context.Context, tx *sqlx.Tx, address, subject string, key ssh.PublicKey, actor string) error {
	var cur hostRow
	err := tx.GetContext(ctx, &cur, `SELECT `+hostColumns+` FROM known_hosts WHERE address = ?`, address)
	switch {
	case err == nil:
		if bytes.Equal(cur.PublicKey, key.Marshal()) {
			return nil
		}
		return ErrHostKnown
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	now, fp := s.now(), ssh.FingerprintSHA256(key)
	if _, err := tx.ExecContext(ctx, `INSERT INTO known_hosts
		(address, subject, key_type, public_key, fingerprint, first_seen_at, accepted_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, address, subject, key.Type(), key.Marshal(), fp, now, now, now); err != nil {
		return err
	}
	_, err = s.ev.Record(ctx, tx, events.Event{
		Type: "ssh.host_key_pinned", Subject: hostSubject(address), Actor: actor,
		Payload: map[string]any{"address": address, "fingerprint": fp},
	})
	return err
}

// Accept trusts the pending key of a host whose key changed.
func (s *SSH) Accept(ctx context.Context, id int64, actor string) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		var r hostRow
		if err := tx.GetContext(ctx, &r, `SELECT `+hostColumns+` FROM known_hosts WHERE id = ?`, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if r.PendingFingerprint == "" {
			return ErrNoPending
		}
		_, err := tx.ExecContext(ctx, `UPDATE known_hosts SET key_type = pending_key_type, public_key = pending_public_key,
			fingerprint = pending_fingerprint, accepted_at = ?, pending_key_type = NULL, pending_public_key = NULL,
			pending_fingerprint = NULL, pending_seen_at = NULL WHERE id = ?`, s.now(), id)
		if err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{
			Type: "ssh.host_key_accepted", Subject: hostSubject(r.Address), Actor: actor,
			Payload: map[string]any{"address": r.Address, "old": r.Fingerprint, "new": r.PendingFingerprint},
		})
		return err
	})
}

// Forget removes a pin: the next contact is a first contact.
func (s *SSH) Forget(ctx context.Context, id int64, actor string) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		var r hostRow
		if err := tx.GetContext(ctx, &r, `SELECT `+hostColumns+` FROM known_hosts WHERE id = ?`, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		return s.forgetTx(ctx, tx, r, actor)
	})
}

func (s *SSH) forgetTx(ctx context.Context, tx *sqlx.Tx, r hostRow, actor string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM known_hosts WHERE id = ?`, r.ID); err != nil {
		return err
	}
	_, err := s.ev.Record(ctx, tx, events.Event{
		Type: "ssh.host_forgotten", Subject: hostSubject(r.Address), Actor: actor,
		Payload: map[string]any{"address": r.Address, "fingerprint": r.Fingerprint},
	})
	return err
}

// ForgetSubject forgets every host pinned for a subject ("server:12"). The
// servers module calls it when it retires a server, so a reused address is a
// first contact and not a changed key.
func (s *SSH) ForgetSubject(ctx context.Context, subject, actor string) error {
	if subject == "" {
		return nil
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		var rows []hostRow
		if err := tx.SelectContext(ctx, &rows, `SELECT `+hostColumns+` FROM known_hosts WHERE subject = ? ORDER BY id`, subject); err != nil {
			return err
		}
		for _, r := range rows {
			if err := s.forgetTx(ctx, tx, r, actor); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetSubject changes whom a pinned address belongs to ("router:new" becomes
// "router:3" once the router is saved). Bookkeeping: it records nothing.
func (s *SSH) SetSubject(ctx context.Context, address, subject string) error {
	address = NormalizeAddress(address)
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE known_hosts SET subject = ? WHERE address = ?`, subject, address)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// KnownHost reads the pin of one address; ErrNotFound when it has none.
func (s *SSH) KnownHost(ctx context.Context, address string) (KnownHost, error) {
	r, err := s.known(ctx, NormalizeAddress(address))
	if err != nil {
		return KnownHost{}, err
	}
	if r == nil {
		return KnownHost{}, ErrNotFound
	}
	return r.host(), nil
}

// touch notes that a pinned host was used; it is bookkeeping and records nothing.
func (s *SSH) touch(ctx context.Context, address string) {
	err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE known_hosts SET last_used_at = ? WHERE address = ?`, s.now(), address)
		return err
	})
	if err != nil {
		s.log.Warn("sshx: known host last use", "address", address, "error", err)
	}
}

// recordChange stores a changed key as pending and records the event, but only
// when this pending key is new: ten jobs meeting the same change notify once.
func (s *SSH) recordChange(ctx context.Context, address string, key ssh.PublicKey) (old string, err error) {
	fp := ssh.FingerprintSHA256(key)
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		var r hostRow
		if err := tx.GetContext(ctx, &r, `SELECT `+hostColumns+` FROM known_hosts WHERE address = ?`, address); err != nil {
			return err
		}
		old = r.Fingerprint
		if r.PendingFingerprint == fp || r.Fingerprint == fp {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE known_hosts SET pending_key_type = ?, pending_public_key = ?,
			pending_fingerprint = ?, pending_seen_at = ? WHERE id = ?`, key.Type(), key.Marshal(), fp, s.now(), r.ID); err != nil {
			return err
		}
		_, err := s.ev.Record(ctx, tx, events.Event{
			Type: "ssh.host_key_changed", Subject: hostSubject(address), Actor: events.ActorSystem,
			Payload: map[string]any{"address": address, "old": r.Fingerprint, "new": fp},
		})
		return err
	})
	return old, err
}

// hostKeyAlgorithms are the signature algorithms to offer for a key type.
func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// firstContactAlgorithms is what a first contact offers, in this order.
var firstContactAlgorithms = []string{
	ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

// hostCheck remembers what the host-key callback saw. The callback runs in
// the middle of the handshake, so it only compares: writing is for afterwards.
type hostCheck struct {
	address string
	known   *hostRow
	mode    FirstContact

	key     ssh.PublicKey
	changed bool
	unknown *UnknownHostError
}

func (h *hostCheck) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	h.key = key
	if h.known == nil {
		if h.mode == ConfirmFirstContact {
			h.unknown = &UnknownHostError{Address: h.address, Fingerprint: ssh.FingerprintSHA256(key), Key: key}
			return h.unknown
		}
		return nil
	}
	if bytes.Equal(key.Marshal(), h.known.PublicKey) {
		return nil
	}
	h.changed = true
	return &HostKeyChangedError{Address: h.address, Old: h.known.Fingerprint, New: ssh.FingerprintSHA256(key)}
}
