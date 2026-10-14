package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// SetupLinkTTL is how long a new member's setup link works.
const SetupLinkTTL = 24 * time.Hour

var (
	ErrUsernameLength      = errors.New("auth: a username is 1 to 64 characters")
	ErrUsernameTaken       = errors.New("auth: the username is taken")
	ErrOwnAccess           = errors.New("auth: a member can't change their own access")
	ErrLastSettingsManager = errors.New("auth: the last member who manages Settings")
	ErrNotAllowed          = errors.New("auth: needs settings: manage")
	ErrSetupLink           = errors.New("auth: the setup link is unknown, used or expired")
	ErrAlreadySetUp        = errors.New("auth: the member has finished setup")
)

// OverreachError: the change gives levels above the grant of whoever makes
// it, in Areas.
type OverreachError struct{ Areas []access.Area }

func (e *OverreachError) Error() string {
	return fmt.Sprintf("auth: a level above your own in %v", e.Areas)
}

// Grant is a member's levels. A removed member has none.
func (s *Service) Grant(ctx context.Context, memberID int64) (access.Grant, error) {
	return readGrant(ctx, s.db.R, memberID)
}

func readGrant(ctx context.Context, q sqlx.QueryerContext, memberID int64) (access.Grant, error) {
	var rows []struct {
		Area  string `db:"area"`
		Level string `db:"level"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT area, level FROM member_access WHERE member_id = ?`, memberID); err != nil {
		return access.Grant{}, err
	}
	var g access.Grant
	for _, r := range rows {
		a, err := access.ParseArea(r.Area)
		if err != nil {
			return access.Grant{}, err
		}
		l, err := access.ParseLevel(a, r.Level)
		if err != nil {
			return access.Grant{}, err
		}
		g = g.With(a, l)
	}
	return g, nil
}

// writeGrant replaces a member's levels; none is no row.
func writeGrant(ctx context.Context, tx *sqlx.Tx, memberID int64, g access.Grant) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM member_access WHERE member_id = ?`, memberID); err != nil {
		return err
	}
	for _, a := range access.Areas() {
		if l := g.Level(a); l != access.LevelNone {
			if _, err := tx.ExecContext(ctx, `INSERT INTO member_access (member_id, area, level) VALUES (?, ?, ?)`, memberID, string(a), string(l)); err != nil {
				return err
			}
		}
	}
	return nil
}

// grantLabel is how an event payload names a grant: its preset, or every
// area's level.
func grantLabel(g access.Grant) string {
	if p := access.PresetOf(g); p != "" {
		return string(p)
	}
	return g.String()
}

// RosterEntry is one row of Settings → Members.
type RosterEntry struct {
	Member
	Grant access.Grant
	// SetupExpires is when the open setup link stops working; zero without
	// one.
	SetupExpires db.Time
}

// Roster lists every member, removed ones too, in the order they were added.
func (s *Service) Roster(ctx context.Context) ([]RosterEntry, error) {
	var rows []memberRow
	if err := sqlx.SelectContext(ctx, s.db.R, &rows, `SELECT `+memberCols+` FROM members ORDER BY id`); err != nil {
		return nil, err
	}
	var links []struct {
		Member  int64   `db:"member_id"`
		Expires db.Time `db:"expires_at"`
	}
	if err := sqlx.SelectContext(ctx, s.db.R, &links, `SELECT member_id, expires_at FROM member_setup_links WHERE ended_at IS NULL`); err != nil {
		return nil, err
	}
	expires := map[int64]db.Time{}
	for _, l := range links {
		expires[l.Member] = l.Expires
	}
	out := make([]RosterEntry, len(rows))
	for i, r := range rows {
		g, err := s.Grant(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out[i] = RosterEntry{Member: r.member(), Grant: g, SetupExpires: expires[r.ID]}
	}
	return out, nil
}

// requireSettingsManager checks, inside the change, that by is a member in
// with settings: manage, and returns their grant.
func requireSettingsManager(ctx context.Context, tx *sqlx.Tx, by int64) (access.Grant, error) {
	var in int
	if err := sqlx.GetContext(ctx, tx, &in, `SELECT COUNT(*) FROM members WHERE id = ? AND removed_at IS NULL AND password_hash IS NOT NULL`, by); err != nil {
		return access.Grant{}, err
	}
	g, err := readGrant(ctx, tx, by)
	if err != nil {
		return g, err
	}
	if in == 0 || !g.Can(access.Settings, access.LevelManage) {
		return g, ErrNotAllowed
	}
	return g, nil
}

// settingsManagers counts the members in, set up, with settings: manage,
// leaving out except.
func settingsManagers(ctx context.Context, tx *sqlx.Tx, except int64) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, tx, &n, `
		SELECT COUNT(*) FROM members m JOIN member_access a ON a.member_id = m.id
		WHERE a.area = 'settings' AND a.level = 'manage' AND m.removed_at IS NULL AND m.password_hash IS NOT NULL AND m.id != ?`, except)
	return n, err
}

func memberTx(ctx context.Context, tx *sqlx.Tx, id int64) (Member, error) {
	var r memberRow
	err := sqlx.GetContext(ctx, tx, &r, `SELECT `+memberCols+` FROM members WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrNoMember
	}
	return r.member(), err
}

// AddMember adds a member with grant g and makes their setup link; it
// returns the new member's ID and the link's token. The one adding them
// re-enters their password: a setup link is a new way in. Nobody but the
// new member ever handles their password.
func (s *Service) AddMember(ctx context.Context, by Session, password, username string, g access.Grant) (int64, string, error) {
	username, err := checkUsername(username)
	if err != nil {
		return 0, "", err
	}
	if err := s.checkCurrentPassword(ctx, by.MemberID, password); err != nil {
		return 0, "", err
	}
	lang, err := s.settings.Get(ctx, "general.language")
	if err != nil {
		return 0, "", err
	}
	var id int64
	var token string
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		mine, err := requireSettingsManager(ctx, tx, by.MemberID)
		if err != nil {
			return err
		}
		if over := access.Overreach(mine, access.Grant{}, g); len(over) > 0 {
			return &OverreachError{Areas: over}
		}
		var taken int
		if err := sqlx.GetContext(ctx, tx, &taken, `SELECT COUNT(*) FROM members WHERE username = ? AND removed_at IS NULL`, username); err != nil {
			return err
		}
		if taken > 0 {
			return ErrUsernameTaken
		}
		actor := events.MemberActor(by.MemberID)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO members (username, language, webauthn_user_id, created_at, created_by)
			VALUES (?, ?, randomblob(32), ?, ?)`, username, lang, s.now(), actor)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if err := writeGrant(ctx, tx, id, g); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "member.added", actor, MemberSubject(id), map[string]any{"username": username, "access": grantLabel(g)}); err != nil {
			return err
		}
		token, err = s.newSetupLink(ctx, tx, id, username, actor)
		return err
	})
	return id, token, err
}

// NewSetupLink replaces the setup link of a member who hasn't finished
// setup (the first one ran out, or was lost) and returns the new token.
func (s *Service) NewSetupLink(ctx context.Context, by Session, password string, memberID int64) (string, error) {
	if err := s.checkCurrentPassword(ctx, by.MemberID, password); err != nil {
		return "", err
	}
	var token string
	err := s.db.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := requireSettingsManager(ctx, tx, by.MemberID); err != nil {
			return err
		}
		m, err := memberTx(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if m.Removed() {
			return ErrNoMember
		}
		if m.SetUp() {
			return ErrAlreadySetUp
		}
		if err := s.endSetupLinks(ctx, tx, memberID, "replaced"); err != nil {
			return err
		}
		token, err = s.newSetupLink(ctx, tx, memberID, m.Username, events.MemberActor(by.MemberID))
		return err
	})
	return token, err
}

func setupLookup(v *vault.Vault, token string) []byte { return v.Lookup("setup\x00" + token) }

func setupLinkAAD(id int64) string { return "setup_link:" + strconv.FormatInt(id, 10) + ":token" }

func (s *Service) newSetupLink(ctx context.Context, tx *sqlx.Tx, memberID int64, username, actor string) (string, error) {
	token := vault.NewToken()
	now := s.Now()
	expires := db.At(now.Add(SetupLinkTTL))
	res, err := tx.ExecContext(ctx, `
		INSERT INTO member_setup_links (member_id, token_lookup, created_at, created_by, expires_at)
		VALUES (?, ?, ?, ?, ?)`, memberID, setupLookup(s.vault, token), db.At(now), actor, expires)
	if err != nil {
		return "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE member_setup_links SET token = ? WHERE id = ?`, s.vault.SealString(token, setupLinkAAD(id)), id); err != nil {
		return "", err
	}
	return token, s.record(ctx, tx, "member.setup_link_created", actor, MemberSubject(memberID), map[string]any{"username": username, "expires": expires.Format(db.TimeLayout)})
}

// endSetupLinks ends a member's open link and erases its token.
func (s *Service) endSetupLinks(ctx context.Context, tx *sqlx.Tx, memberID int64, why string) error {
	_, err := tx.ExecContext(ctx, `UPDATE member_setup_links SET ended_at = ?, end_reason = ?, token = NULL WHERE member_id = ? AND ended_at IS NULL`,
		s.now(), why, memberID)
	return err
}

// SetupLinkToken is the token of a member's open setup link while it works,
// for the Members page to copy again.
func (s *Service) SetupLinkToken(ctx context.Context, memberID int64) (token string, expires db.Time, ok bool, err error) {
	var r struct {
		ID      int64   `db:"id"`
		Token   []byte  `db:"token"`
		Expires db.Time `db:"expires_at"`
	}
	err = sqlx.GetContext(ctx, s.db.R, &r, `SELECT id, token, expires_at FROM member_setup_links WHERE member_id = ? AND ended_at IS NULL`, memberID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", db.Time{}, false, nil
	}
	if err != nil || !s.Now().Before(r.Expires.Time) || r.Token == nil {
		return "", r.Expires, false, err
	}
	token, err = s.vault.OpenString(r.Token, setupLinkAAD(r.ID))
	return token, r.Expires, err == nil, err
}

// PendingMember is what a working setup link shows: whose it is.
type PendingMember struct {
	MemberID int64
	Username string
	Expires  db.Time
}

// setupLinkTx finds a setup link that still works: open, not expired, of a
// member who is in and has no password yet. Anything else is ErrSetupLink.
func (s *Service) setupLinkTx(ctx context.Context, q sqlx.QueryerContext, token string) (PendingMember, int64, error) {
	var r struct {
		ID       int64   `db:"id"`
		Member   int64   `db:"member_id"`
		Username string  `db:"username"`
		Expires  db.Time `db:"expires_at"`
	}
	err := sqlx.GetContext(ctx, q, &r, `
		SELECT l.id, l.member_id, m.username, l.expires_at FROM member_setup_links l JOIN members m ON m.id = l.member_id
		WHERE l.token_lookup = ? AND l.ended_at IS NULL AND m.removed_at IS NULL AND m.password_hash IS NULL`, setupLookup(s.vault, token))
	if errors.Is(err, sql.ErrNoRows) {
		return PendingMember{}, 0, ErrSetupLink
	}
	if err != nil {
		return PendingMember{}, 0, err
	}
	if !s.Now().Before(r.Expires.Time) {
		return PendingMember{}, 0, ErrSetupLink
	}
	return PendingMember{MemberID: r.Member, Username: r.Username, Expires: r.Expires}, r.ID, nil
}

// SetupLink finds a setup link that still works, or ErrSetupLink.
func (s *Service) SetupLink(ctx context.Context, token string) (PendingMember, error) {
	if token == "" {
		return PendingMember{}, ErrSetupLink
	}
	inv, _, err := s.setupLinkTx(ctx, s.db.R, token)
	return inv, err
}

// CompleteSetup sets the new member's password and language through their
// setup link, ends the link and signs them in.
func (s *Service) CompleteSetup(ctx context.Context, token, password string, lang i18n.Lang, ip, userAgent string) (SignInResult, error) {
	if err := checkPassword(password); err != nil {
		return SignInResult{}, err
	}
	if !lang.Valid() {
		lang = i18n.EN
	}
	if _, err := s.SetupLink(ctx, token); err != nil {
		return SignInResult{}, err
	}
	phc, err := s.hash(ctx, password)
	if err != nil {
		return SignInResult{}, err
	}
	var res SignInResult
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		inv, linkID, err := s.setupLinkTx(ctx, tx, token)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET password_hash = ?, password_changed_at = ?, language = ? WHERE id = ?`,
			phc, s.now(), string(lang), inv.MemberID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE member_setup_links SET ended_at = ?, end_reason = 'used', token = NULL WHERE id = ?`, s.now(), linkID); err != nil {
			return err
		}
		actor := events.MemberActor(inv.MemberID)
		if err := s.record(ctx, tx, "member.setup_completed", actor, MemberSubject(inv.MemberID), map[string]any{"username": inv.Username}); err != nil {
			return err
		}
		res, err = s.createSessionTx(ctx, tx, inv.MemberID, ip, userAgent, MethodPassword)
		return err
	})
	return res, err
}

// SetAccess changes another member's levels. A member can't change their
// own, can't raise anyone above their own level, and can't take Settings
// from the last member who manages it. The same levels record nothing.
func (s *Service) SetAccess(ctx context.Context, by, memberID int64, g access.Grant) error {
	if by == memberID {
		return ErrOwnAccess
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		mine, err := requireSettingsManager(ctx, tx, by)
		if err != nil {
			return err
		}
		m, err := memberTx(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if m.Removed() {
			return ErrNoMember
		}
		from, err := readGrant(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if from == g {
			return nil
		}
		if over := access.Overreach(mine, from, g); len(over) > 0 {
			return &OverreachError{Areas: over}
		}
		if m.SetUp() && from.Can(access.Settings, access.LevelManage) && !g.Can(access.Settings, access.LevelManage) {
			if n, err := settingsManagers(ctx, tx, memberID); err != nil {
				return err
			} else if n == 0 {
				return ErrLastSettingsManager
			}
		}
		if err := writeGrant(ctx, tx, memberID, g); err != nil {
			return err
		}
		return s.record(ctx, tx, "member.access_changed", events.MemberActor(by), MemberSubject(memberID),
			map[string]any{"username": m.Username, "changes": strings.Join(access.Changes(from, g), ", "), "access": grantLabel(g)})
	})
}

// Removal is what RemoveMember did.
type Removal struct {
	Username string
	// Heir is the member who gets everything the removed one owned: who
	// removed them, or for a member who removed themselves, the first
	// member in who manages Settings.
	Heir     int64
	Sessions int
}

// RemoveMember removes a member: their sessions end, their passkeys, their
// authenticator app, their levels and their setup link go, and the row
// stays, removed, so history names them. The last member who manages
// Settings can't be removed. member.removed names the heir, for the
// modules to move what the removed member owned.
func (s *Service) RemoveMember(ctx context.Context, by, memberID int64) (Removal, error) {
	var r Removal
	err := s.db.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := requireSettingsManager(ctx, tx, by); err != nil {
			return err
		}
		m, err := memberTx(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if m.Removed() {
			return ErrNoMember
		}
		r.Username = m.Username
		g, err := readGrant(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if m.SetUp() && g.Can(access.Settings, access.LevelManage) {
			if n, err := settingsManagers(ctx, tx, memberID); err != nil {
				return err
			} else if n == 0 {
				return ErrLastSettingsManager
			}
		}
		r.Heir = by
		if by == memberID {
			if err := sqlx.GetContext(ctx, tx, &r.Heir, `
				SELECT m.id FROM members m JOIN member_access a ON a.member_id = m.id
				WHERE a.area = 'settings' AND a.level = 'manage' AND m.removed_at IS NULL AND m.password_hash IS NOT NULL AND m.id != ?
				ORDER BY m.id LIMIT 1`, memberID); err != nil {
				return err
			}
		}
		heir, err := memberTx(ctx, tx, r.Heir)
		if err != nil {
			return err
		}
		actor := events.MemberActor(by)
		if _, err := tx.ExecContext(ctx, `UPDATE members SET removed_at = ?, removed_by = ? WHERE id = ?`, s.now(), actor, memberID); err != nil {
			return err
		}
		for _, q := range []string{`DELETE FROM member_access WHERE member_id = ?`, `DELETE FROM totp WHERE member_id = ?`, `DELETE FROM passkeys WHERE member_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, memberID); err != nil {
				return err
			}
		}
		if err := s.endSetupLinks(ctx, tx, memberID, "removed"); err != nil {
			return err
		}
		if r.Sessions, err = s.endLive(ctx, tx, memberID, EndMemberRemoved, 0); err != nil {
			return err
		}
		return s.record(ctx, tx, "member.removed", actor, MemberSubject(memberID),
			map[string]any{"username": m.Username, "heir": r.Heir, "heir_username": heir.Username, "sessions": r.Sessions})
	})
	return r, err
}
