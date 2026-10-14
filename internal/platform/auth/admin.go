package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// AdminExists reports whether the admin was created.
func (s *Service) AdminExists(ctx context.Context) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, s.db.R, &n, `SELECT COUNT(*) FROM admin`)
	return n > 0, err
}

// CreateAdmin makes the one admin, in the language of general.language.
func (s *Service) CreateAdmin(ctx context.Context, username, password string) error {
	if len(username) == 0 || len(username) > 64 {
		return errors.New("auth: username must be 1 to 64 characters")
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	if ok, err := s.AdminExists(ctx); err != nil {
		return err
	} else if ok {
		return ErrAdminExists
	}
	lang, err := s.settings.Get(ctx, "general.language")
	if err != nil {
		return err
	}
	phc, err := s.hash(ctx, password)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var n int
		if err := sqlx.GetContext(ctx, tx, &n, `SELECT COUNT(*) FROM admin`); err != nil {
			return err
		}
		if n > 0 {
			return ErrAdminExists
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at, webauthn_user_id) VALUES (1, ?, ?, ?, ?, ?, randomblob(32))`,
			username, phc, lang, now, now); err != nil {
			return err
		}
		return s.record(ctx, tx, "admin.created", events.ActorCLI, adminSubject, map[string]any{"username": username})
	})
}

// ResetPassword sets a new password and ends every session. A lockout keeps
// applying: attempts are not touched.
func (s *Service) ResetPassword(ctx context.Context, password string) (ended int, err error) {
	if err := checkPassword(password); err != nil {
		return 0, err
	}
	if ok, err := s.AdminExists(ctx); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrNoAdmin
	}
	phc, err := s.hash(ctx, password)
	if err != nil {
		return 0, err
	}
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		if err := s.setPassword(ctx, tx, phc); err != nil {
			return err
		}
		if ended, err = s.endLive(ctx, tx, EndPasswordReset, 0); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.password_changed", events.ActorCLI, adminSubject, map[string]any{"via": "cli"})
	})
	return ended, err
}

// SecondFactorReset is what ResetSecondFactor removed and ended.
type SecondFactorReset struct {
	TOTP     bool
	Passkeys int
	Sessions int
}

// ResetSecondFactor is the way back in without the phone or the passkeys:
// from the container shell it turns the authenticator app off, removes every
// passkey and ends every session. The password stays.
func (s *Service) ResetSecondFactor(ctx context.Context) (SecondFactorReset, error) {
	if ok, err := s.AdminExists(ctx); err != nil {
		return SecondFactorReset{}, err
	} else if !ok {
		return SecondFactorReset{}, ErrNoAdmin
	}
	var r SecondFactorReset
	err := s.db.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM totp`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		r.TOTP = n > 0
		if res, err = tx.ExecContext(ctx, `DELETE FROM passkeys`); err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		r.Passkeys = int(n)
		if r.Sessions, err = s.endLive(ctx, tx, EndSecondFactorReset, 0); err != nil {
			return err
		}
		if r == (SecondFactorReset{}) {
			return nil
		}
		return s.record(ctx, tx, "auth.second_factor_reset", events.ActorCLI, adminSubject,
			map[string]any{"via": "cli", "totp": r.TOTP, "passkeys": r.Passkeys, "sessions": r.Sessions})
	})
	return r, err
}

func (s *Service) setPassword(ctx context.Context, tx *sqlx.Tx, phc string) error {
	_, err := tx.ExecContext(ctx, `UPDATE admin SET password_hash = ?, password_changed_at = ? WHERE id = 1`, phc, s.now())
	return err
}

// ChangePassword needs the current password and ends every session except
// the one making the change.
func (s *Service) ChangePassword(ctx context.Context, current int64, old, new string) error {
	if err := s.checkCurrentPassword(ctx, old); err != nil {
		return err
	}
	if err := checkPassword(new); err != nil {
		return err
	}
	hashed, err := s.hash(ctx, new)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		if err := s.setPassword(ctx, tx, hashed); err != nil {
			return err
		}
		if _, err := s.endLive(ctx, tx, EndPasswordChanged, current); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.password_changed", events.ActorAdmin, adminSubject, map[string]any{"via": "ui"})
	})
}

// Admin returns the admin, or ErrNoAdmin.
func (s *Service) Admin(ctx context.Context) (Admin, error) {
	var r struct {
		Username string  `db:"username"`
		Language string  `db:"language"`
		Created  db.Time `db:"created_at"`
		Changed  db.Time `db:"password_changed_at"`
	}
	err := sqlx.GetContext(ctx, s.db.R, &r, `SELECT username, language, created_at, password_changed_at FROM admin WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, ErrNoAdmin
	}
	return Admin{Username: r.Username, Language: i18n.Lang(r.Language), CreatedAt: r.Created, PasswordChangedAt: r.Changed}, err
}

// SetLanguage changes the admin's language; the same one records nothing.
func (s *Service) SetLanguage(ctx context.Context, l i18n.Lang) error {
	if !l.Valid() {
		return errors.New("auth: unsupported language")
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var from string
		if err := sqlx.GetContext(ctx, tx, &from, `SELECT language FROM admin WHERE id = 1`); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoAdmin
			}
			return err
		}
		if from == string(l) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE admin SET language = ? WHERE id = 1`, string(l)); err != nil {
			return err
		}
		return s.record(ctx, tx, "admin.language_changed", events.ActorAdmin, adminSubject, map[string]any{"from": from, "to": string(l)})
	})
}
