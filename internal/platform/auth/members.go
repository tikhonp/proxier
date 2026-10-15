package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// HasMembers reports whether the first member was created.
func (s *Service) HasMembers(ctx context.Context) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, s.db.R, &n, `SELECT COUNT(*) FROM members`)
	return n > 0, err
}

// CreateFirstMember makes the first member, as Owner, in the language of
// general.language. It refuses once any member exists.
func (s *Service) CreateFirstMember(ctx context.Context, username, password string) error {
	username, err := checkUsername(username)
	if err != nil {
		return err
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	if ok, err := s.HasMembers(ctx); err != nil {
		return err
	} else if ok {
		return ErrMembersExist
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
		if err := sqlx.GetContext(ctx, tx, &n, `SELECT COUNT(*) FROM members`); err != nil {
			return err
		}
		if n > 0 {
			return ErrMembersExist
		}
		now := s.now()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO members (username, password_hash, language, webauthn_user_id, created_at, created_by, password_changed_at)
			VALUES (?, ?, ?, randomblob(32), ?, ?, ?)`,
			username, phc, lang, now, events.ActorCLI, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		owner := access.PresetOwner.Grant()
		if err := writeGrant(ctx, tx, id, owner); err != nil {
			return err
		}
		return s.record(ctx, tx, "member.added", events.ActorCLI, MemberSubject(id), map[string]any{"username": username, "access": grantLabel(owner)})
	})
}

// checkUsername trims a username and checks its length.
func checkUsername(u string) (string, error) {
	u = strings.TrimSpace(u)
	if n := utf8.RuneCountInString(u); n == 0 || n > 64 {
		return "", ErrUsernameLength
	}
	return u, nil
}

// ResetPassword sets a new password and ends every session of the member. A
// lockout keeps applying: attempts are not touched.
func (s *Service) ResetPassword(ctx context.Context, memberID int64, password string) (ended int, err error) {
	if err := checkPassword(password); err != nil {
		return 0, err
	}
	if _, err := s.Member(ctx, memberID); err != nil {
		return 0, err
	}
	phc, err := s.hash(ctx, password)
	if err != nil {
		return 0, err
	}
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		if err := s.setPassword(ctx, tx, memberID, phc); err != nil {
			return err
		}
		if ended, err = s.endLive(ctx, tx, memberID, EndPasswordReset, 0); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.password_changed", events.ActorCLI, MemberSubject(memberID), map[string]any{"via": "cli"})
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
// from the container shell it turns the member's authenticator app off,
// removes their passkeys and ends their sessions. The password stays.
func (s *Service) ResetSecondFactor(ctx context.Context, memberID int64) (SecondFactorReset, error) {
	if _, err := s.Member(ctx, memberID); err != nil {
		return SecondFactorReset{}, err
	}
	var r SecondFactorReset
	err := s.db.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM totp WHERE member_id = ?`, memberID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		r.TOTP = n > 0
		if res, err = tx.ExecContext(ctx, `DELETE FROM passkeys WHERE member_id = ?`, memberID); err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		r.Passkeys = int(n)
		if r.Sessions, err = s.endLive(ctx, tx, memberID, EndSecondFactorReset, 0); err != nil {
			return err
		}
		if r == (SecondFactorReset{}) {
			return nil
		}
		return s.record(ctx, tx, "auth.second_factor_reset", events.ActorCLI, MemberSubject(memberID),
			map[string]any{"via": "cli", "totp": r.TOTP, "passkeys": r.Passkeys, "sessions": r.Sessions})
	})
	return r, err
}

func (s *Service) setPassword(ctx context.Context, tx *sqlx.Tx, memberID int64, phc string) error {
	_, err := tx.ExecContext(ctx, `UPDATE members SET password_hash = ?, password_changed_at = ? WHERE id = ?`, phc, s.now(), memberID)
	return err
}

// ChangePassword needs the current password and ends every session of the
// member except the one making the change.
func (s *Service) ChangePassword(ctx context.Context, sess Session, old, new string) error {
	if err := s.checkCurrentPassword(ctx, sess.MemberID, old); err != nil {
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
		if err := s.setPassword(ctx, tx, sess.MemberID, hashed); err != nil {
			return err
		}
		if _, err := s.endLive(ctx, tx, sess.MemberID, EndPasswordChanged, sess.ID); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.password_changed", events.MemberActor(sess.MemberID), MemberSubject(sess.MemberID), map[string]any{"via": "ui"})
	})
}

type memberRow struct {
	ID        int64    `db:"id"`
	Username  string   `db:"username"`
	Language  string   `db:"language"`
	Created   db.Time  `db:"created_at"`
	CreatedBy string   `db:"created_by"`
	Changed   *db.Time `db:"password_changed_at"`
	Removed   *db.Time `db:"removed_at"`
	ChatID    string   `db:"telegram_chat_id"`
	ChatTitle string   `db:"telegram_chat_title"`
}

const memberCols = `id, username, language, created_at, created_by, password_changed_at, removed_at, telegram_chat_id, telegram_chat_title`

func (r memberRow) member() Member {
	m := Member{ID: r.ID, Username: r.Username, Language: i18n.Lang(r.Language), CreatedAt: r.Created, CreatedBy: r.CreatedBy,
		TelegramChatID: r.ChatID, TelegramChatTitle: r.ChatTitle}
	if r.Changed != nil {
		m.PasswordChangedAt = *r.Changed
	}
	if r.Removed != nil {
		m.RemovedAt = *r.Removed
	}
	return m
}

// Member returns one member, or ErrNoMember.
func (s *Service) Member(ctx context.Context, id int64) (Member, error) {
	var r memberRow
	err := sqlx.GetContext(ctx, s.db.R, &r, `SELECT `+memberCols+` FROM members WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrNoMember
	}
	return r.member(), err
}

// MemberByUsername finds a member who isn't removed by username, ignoring
// case, or ErrNoMember. A removed member's username may belong to someone
// else now.
func (s *Service) MemberByUsername(ctx context.Context, username string) (Member, error) {
	var r memberRow
	err := sqlx.GetContext(ctx, s.db.R, &r, `SELECT `+memberCols+` FROM members WHERE username = ? AND removed_at IS NULL`, username)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrNoMember
	}
	return r.member(), err
}

// Usernames maps every member's ID to their username, to name actors; a
// removed member keeps theirs.
func (s *Service) Usernames(ctx context.Context) (map[int64]string, error) {
	var rows []memberRow
	if err := sqlx.SelectContext(ctx, s.db.R, &rows, `SELECT `+memberCols+` FROM members`); err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Username
	}
	return out, nil
}

// SetLanguage changes a member's language; the same one records nothing.
func (s *Service) SetLanguage(ctx context.Context, memberID int64, l i18n.Lang) error {
	if !l.Valid() {
		return errors.New("auth: unsupported language")
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var from string
		if err := sqlx.GetContext(ctx, tx, &from, `SELECT language FROM members WHERE id = ?`, memberID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoMember
			}
			return err
		}
		if from == string(l) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET language = ? WHERE id = ?`, string(l), memberID); err != nil {
			return err
		}
		return s.record(ctx, tx, "member.language_changed", events.MemberActor(memberID), MemberSubject(memberID), map[string]any{"from": from, "to": string(l)})
	})
}

// SetTelegramChat changes where a member's notifications go; "" for
// nowhere. The caller checks the chat ID's form, which is the channel's.
// The same chat records nothing.
func (s *Service) SetTelegramChat(ctx context.Context, memberID int64, chatID, title string) error {
	if len(chatID) > 64 || len(title) > 128 {
		return errors.New("auth: a chat ID is at most 64 bytes and its title 128")
	}
	if chatID == "" {
		title = ""
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var cur struct {
			ID    string `db:"telegram_chat_id"`
			Title string `db:"telegram_chat_title"`
		}
		if err := sqlx.GetContext(ctx, tx, &cur, `SELECT telegram_chat_id, telegram_chat_title FROM members WHERE id = ? AND removed_at IS NULL`, memberID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoMember
			}
			return err
		}
		if cur.ID == chatID && cur.Title == title {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET telegram_chat_id = ?, telegram_chat_title = ? WHERE id = ?`, chatID, title, memberID); err != nil {
			return err
		}
		return s.record(ctx, tx, NotificationsChangedEvent, events.MemberActor(memberID), MemberSubject(memberID), map[string]any{"keys": "telegram.chat"})
	})
}
