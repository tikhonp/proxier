package auth_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
)

// owner is member 1's session.
func (e *env) owner() auth.Session {
	e.t.Helper()
	return e.signIn("10.0.0.1").Session
}

// invite adds a member through the setup flow and returns their ID and token.
func (e *env) invite(by auth.Session, username string, g access.Grant) (int64, string) {
	e.t.Helper()
	id, token, err := e.s.AddMember(e.ctx, by, goodPassword, username, g)
	if err != nil {
		e.t.Fatalf("add %s: %v", username, err)
	}
	return id, token
}

// join adds a member and finishes their setup.
func (e *env) join(by auth.Session, username string, g access.Grant) (int64, auth.Session) {
	e.t.Helper()
	id, token := e.invite(by, username, g)
	res, err := e.s.CompleteSetup(e.ctx, token, "their own long password", "ru", "10.0.0.2", "ua")
	if err != nil {
		e.t.Fatalf("setup %s: %v", username, err)
	}
	return id, res.Session
}

func (e *env) grant(id int64) access.Grant {
	e.t.Helper()
	g, err := e.s.Grant(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func TestFirstMemberIsOwner(t *testing.T) {
	e := newEnv(t)
	if e.grant(1) != access.PresetOwner.Grant() {
		t.Fatalf("member 1 = %s", e.grant(1))
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'member.added' AND actor = 'cli' AND json_extract(payload, '$.access') = 'owner'`); n != 1 {
		t.Fatalf("member.added by cli: %d", n)
	}
}

func TestAddingAMemberNeedsThePasswordAndMakesASetupLink(t *testing.T) {
	e := newEnv(t)
	me := e.owner()
	if _, _, err := e.s.AddMember(e.ctx, me, "wrong password!!", "anna", access.PresetViewer.Grant()); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM members`) != 1 {
		t.Fatal("a refused add left a member")
	}
	id, token := e.invite(me, "anna", access.PresetViewer.Grant())
	if e.grant(id) != access.PresetViewer.Grant() || e.events("member.added") != 2 || e.events("member.setup_link_created") != 1 {
		t.Fatalf("added: %s, events %d %d", e.grant(id), e.events("member.added"), e.events("member.setup_link_created"))
	}
	// no password yet: nothing signs anna in
	if _, err := e.s.SignIn(e.ctx, "anna", "", "10.0.0.3", ""); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("sign-in before setup: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM member_setup_links WHERE token_lookup = ? OR token = ?`, token, token); n != 0 {
		t.Fatal("the token is stored in plain text")
	}
	inv, err := e.s.SetupLink(e.ctx, token)
	if err != nil || inv.MemberID != id || inv.Username != "anna" {
		t.Fatalf("setup link: %+v %v", inv, err)
	}
	shown, _, ok, err := e.s.SetupLinkToken(e.ctx, id)
	if err != nil || !ok || shown != token {
		t.Fatalf("the Members page can't copy the link again: %v %v", ok, err)
	}
}

func TestSetupLinkWorksOnce(t *testing.T) {
	e := newEnv(t)
	id, token := e.invite(e.owner(), "anna", access.PresetViewer.Grant())
	res, err := e.s.CompleteSetup(e.ctx, token, "short", "ru", "10.0.0.2", "ua")
	if !errors.Is(err, auth.ErrPasswordTooShort) {
		t.Fatalf("short password: %v", err)
	}
	res, err = e.s.CompleteSetup(e.ctx, token, "their own long password", "ru", "10.0.0.2", "ua")
	if err != nil || res.Session.MemberID != id {
		t.Fatalf("setup: %+v %v", res.Session, err)
	}
	m, _ := e.s.Member(e.ctx, id)
	if !m.SetUp() || m.Language != "ru" || e.events("member.setup_completed") != 1 {
		t.Fatalf("after setup: %+v", m)
	}
	if _, err := e.s.CompleteSetup(e.ctx, token, "another long password", "en", "10.0.0.2", "ua"); !errors.Is(err, auth.ErrSetupLink) {
		t.Fatalf("second use: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM member_setup_links WHERE token IS NOT NULL`) != 0 {
		t.Fatal("a used link keeps its token")
	}
	if _, err := e.s.SignIn(e.ctx, "Anna", "their own long password", "10.0.0.2", ""); err != nil {
		t.Fatalf("sign-in after setup: %v", err)
	}
}

func TestSetupLinkExpiresAfterADayAndCanBeReplaced(t *testing.T) {
	e := newEnv(t)
	me := e.owner()
	id, token := e.invite(me, "anna", access.PresetViewer.Grant())
	e.advance(auth.SetupLinkTTL)
	if _, err := e.s.SetupLink(e.ctx, token); !errors.Is(err, auth.ErrSetupLink) {
		t.Fatalf("expired link: %v", err)
	}
	if _, _, ok, _ := e.s.SetupLinkToken(e.ctx, id); ok {
		t.Fatal("an expired link is offered for copying")
	}
	if _, err := e.s.NewSetupLink(e.ctx, me, "wrong password!!", id); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("new link with a wrong password: %v", err)
	}
	fresh, err := e.s.NewSetupLink(e.ctx, me, goodPassword, id)
	if err != nil {
		t.Fatal(err)
	}
	e.advance(-auth.SetupLinkTTL) // the old one would still be in time: it was replaced
	if _, err := e.s.SetupLink(e.ctx, token); !errors.Is(err, auth.ErrSetupLink) {
		t.Fatalf("replaced link: %v", err)
	}
	e.advance(auth.SetupLinkTTL)
	if _, err := e.s.SetupLink(e.ctx, fresh); err != nil {
		t.Fatalf("new link: %v", err)
	}
	if _, err := e.s.CompleteSetup(e.ctx, fresh, "their own long password", "en", "1.2.3.4", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.NewSetupLink(e.ctx, me, goodPassword, id); !errors.Is(err, auth.ErrAlreadySetUp) {
		t.Fatalf("a link for a member set up: %v", err)
	}
}

func TestATakenUsernameIsAskedAgain(t *testing.T) {
	e := newEnv(t)
	me := e.owner()
	id, _ := e.join(me, "anna", access.PresetViewer.Grant())
	if _, _, err := e.s.AddMember(e.ctx, me, goodPassword, " ANNA ", access.Grant{}); !errors.Is(err, auth.ErrUsernameTaken) {
		t.Fatalf("taken in another case: %v", err)
	}
	if _, _, err := e.s.AddMember(e.ctx, me, goodPassword, "", access.Grant{}); !errors.Is(err, auth.ErrUsernameLength) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := e.s.RemoveMember(e.ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	// removed, anna's username is free: adding her again takes it back
	again, _ := e.invite(me, "anna", access.PresetViewer.Grant())
	if again == id {
		t.Fatal("the ID was reused")
	}
	names, _ := e.s.Usernames(e.ctx)
	if names[id] != "anna" || names[again] != "anna" {
		t.Fatalf("usernames: %v", names)
	}
}

func TestNobodyGivesALevelAboveTheirOwn(t *testing.T) {
	e := newEnv(t)
	limited := access.Grant{}.With(access.Settings, access.LevelManage).With(access.Servers, access.LevelView)
	_, lim := e.join(e.owner(), "lim", limited)

	var over *auth.OverreachError
	if _, _, err := e.s.AddMember(e.ctx, lim, "their own long password", "x", access.PresetOperator.Grant()); !errors.As(err, &over) || !slices.Contains(over.Areas, access.Servers) {
		t.Fatalf("adding an operator: %v", err)
	}
	high, _ := e.join(e.owner(), "high", access.PresetOperator.Grant())
	// lim may change high's routing, keeping servers: manage, which lim lacks
	lower := access.PresetOperator.Grant().With(access.Routing, access.LevelOwn)
	if err := e.s.SetAccess(e.ctx, lim.MemberID, high, lower); err != nil {
		t.Fatalf("keeping a level above lim's: %v", err)
	}
	if err := e.s.SetAccess(e.ctx, lim.MemberID, high, lower.With(access.Routing, access.LevelViewAll)); !errors.As(err, &over) {
		t.Fatalf("raising above lim: %v", err)
	}
	if e.events("member.access_changed") != 1 {
		t.Fatalf("access_changed events: %d", e.events("member.access_changed"))
	}
}

func TestNobodyChangesTheirOwnAccess(t *testing.T) {
	e := newEnv(t)
	if err := e.s.SetAccess(e.ctx, 1, 1, access.PresetViewer.Grant()); !errors.Is(err, auth.ErrOwnAccess) {
		t.Fatalf("own access: %v", err)
	}
	if e.grant(1) != access.PresetOwner.Grant() {
		t.Fatal("changed anyway")
	}
}

func TestSameLevelsRecordNothing(t *testing.T) {
	e := newEnv(t)
	id, _ := e.join(e.owner(), "anna", access.PresetViewer.Grant())
	if err := e.s.SetAccess(e.ctx, 1, id, access.PresetViewer.Grant()); err != nil {
		t.Fatal(err)
	}
	if e.events("member.access_changed") != 0 {
		t.Fatal("an unchanged grant recorded an event")
	}
}

func TestOnlyASettingsManagerChangesMembers(t *testing.T) {
	e := newEnv(t)
	id, viewer := e.join(e.owner(), "anna", access.PresetViewer.Grant())
	if _, _, err := e.s.AddMember(e.ctx, viewer, "their own long password", "x", access.Grant{}); !errors.Is(err, auth.ErrNotAllowed) {
		t.Fatalf("add by a viewer: %v", err)
	}
	if _, err := e.s.RemoveMember(e.ctx, id, 1); !errors.Is(err, auth.ErrNotAllowed) {
		t.Fatalf("remove by a viewer: %v", err)
	}
	if err := e.s.SetAccess(e.ctx, id, 1, access.Grant{}); !errors.Is(err, auth.ErrNotAllowed) {
		t.Fatalf("set access by a viewer: %v", err)
	}
}

// Whoever changes members manages Settings and can't change their own
// levels, so only removing themselves could leave nobody to manage it.
func TestTheLastSettingsManagerStays(t *testing.T) {
	e := newEnv(t)
	me := e.owner()
	if _, err := e.s.RemoveMember(e.ctx, 1, 1); !errors.Is(err, auth.ErrLastSettingsManager) {
		t.Fatalf("removing the only one: %v", err)
	}
	// a manager who hasn't finished setup can't sign in, so doesn't count
	pending, _ := e.invite(me, "pending", access.PresetOwner.Grant())
	if _, err := e.s.RemoveMember(e.ctx, 1, 1); !errors.Is(err, auth.ErrLastSettingsManager) {
		t.Fatalf("removing the only one set up: %v", err)
	}
	other, _ := e.join(me, "other", access.PresetOwner.Grant())
	r, err := e.s.RemoveMember(e.ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Heir != other || r.Heir == pending {
		t.Fatalf("heir of a member who removed themselves: %d, want %d", r.Heir, other)
	}
}

func TestRemovingEndsEveryWayInAndKeepsTheName(t *testing.T) {
	e := newEnv(t)
	id, sess := e.join(e.owner(), "anna", access.PresetOperator.Grant())
	if _, err := e.db.W.ExecContext(e.ctx, `INSERT INTO totp (member_id, secret, created_at) VALUES (?, x'01', '2026-10-01T00:00:00.000Z')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.W.ExecContext(e.ctx, `INSERT INTO passkeys (member_id, credential_id, name, credential, created_at) VALUES (?, x'c1', 'Phone', '{}', '2026-10-01T00:00:00.000Z')`, id); err != nil {
		t.Fatal(err)
	}
	r, err := e.s.RemoveMember(e.ctx, 1, id)
	if err != nil || r.Heir != 1 || r.Sessions != 1 {
		t.Fatalf("remove: %+v %v", r, err)
	}
	if _, err := e.s.Authenticate(e.ctx, sess.Token, "10.0.0.2"); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("anna's session: %v", err)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM sessions WHERE member_id = 2 AND end_reason = 'member_removed'`: 1,
		`SELECT COUNT(*) FROM totp WHERE member_id = 2`:                                       0,
		`SELECT COUNT(*) FROM passkeys WHERE member_id = 2`:                                   0,
		`SELECT COUNT(*) FROM member_access WHERE member_id = 2`:                              0,
		`SELECT COUNT(*) FROM members WHERE id = 2 AND removed_by = 'member:1'`:               1,
		`SELECT COUNT(*) FROM events WHERE type = 'member.removed' AND json_extract(payload, '$.heir') = 1 AND json_extract(payload, '$.username') = 'anna'`: 1,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	if _, err := e.s.SignIn(e.ctx, "anna", "their own long password", "10.0.0.2", ""); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("a removed member signs in: %v", err)
	}
	if names, _ := e.s.Usernames(e.ctx); names[id] != "anna" {
		t.Fatal("history no longer names anna")
	}
	if _, err := e.s.RemoveMember(e.ctx, 1, id); !errors.Is(err, auth.ErrNoMember) {
		t.Fatalf("removing twice: %v", err)
	}
	if err := e.s.SetAccess(e.ctx, 1, id, access.Grant{}); !errors.Is(err, auth.ErrNoMember) {
		t.Fatalf("access of a removed member: %v", err)
	}
	if e.events("member.removed") != 1 {
		t.Fatal("removing twice recorded twice")
	}
}

func TestARemovedMembersSetupLinkStopsWorking(t *testing.T) {
	e := newEnv(t)
	id, token := e.invite(e.owner(), "anna", access.PresetViewer.Grant())
	if _, err := e.s.RemoveMember(e.ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetupLink(e.ctx, token); !errors.Is(err, auth.ErrSetupLink) {
		t.Fatalf("link of a removed member: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM member_setup_links WHERE end_reason = 'removed' AND token IS NULL`) != 1 {
		t.Fatal("the link wasn't ended with its token erased")
	}
}
