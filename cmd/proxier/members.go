package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"golang.org/x/term"
)

// Prompter reads from the terminal; tests use a scripted one.
type Prompter interface {
	Line(prompt string) (string, error)
	Password(prompt string) (string, error) // no echo
}

// maxRounds is how many times a bad password is asked for again.
const maxRounds = 3

var errMembersExist = errors.New("a member already exists")

// createAdmin makes the first member; after that, members are added in the UI.
func createAdmin(ctx context.Context, a *auth.Service, p Prompter, out io.Writer) error {
	if ok, err := a.HasMembers(ctx); err != nil {
		return err
	} else if ok {
		sayln(out, "A member already exists; use reset-password <username> to get back in.")
		return errMembersExist
	}
	username, err := p.Line("Username: ")
	if err != nil {
		return err
	}
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 {
		return errors.New("the username must be 1 to 64 characters")
	}
	pw, err := askPassword(p, out)
	if err != nil {
		return err
	}
	if err := a.CreateFirstMember(ctx, username, pw); err != nil {
		return err
	}
	sayln(out, "First member created. Sign in at your base URL.")
	return nil
}

// member finds the member a command names, saying so when there is none.
func member(ctx context.Context, a *auth.Service, username string, out io.Writer) (auth.Member, error) {
	m, err := a.MemberByUsername(ctx, username)
	if errors.Is(err, auth.ErrNoMember) {
		if ok, herr := a.HasMembers(ctx); herr == nil && !ok {
			sayln(out, "There are no members yet; use create-admin.")
		} else {
			sayf(out, "There is no member %q.\n", username)
		}
	}
	return m, err
}

func resetPassword(ctx context.Context, a *auth.Service, username string, p Prompter, out io.Writer) error {
	m, err := member(ctx, a, username, out)
	if err != nil {
		return err
	}
	pw, err := askPassword(p, out)
	if err != nil {
		return err
	}
	ended, err := a.ResetPassword(ctx, m.ID, pw)
	if err != nil {
		return err
	}
	sayf(out, "Password changed. %d session(s) ended.\n", ended)
	return nil
}

// resetSecondFactor needs no terminal: it asks nothing.
func resetSecondFactor(ctx context.Context, a *auth.Service, username string, out io.Writer) error {
	m, err := member(ctx, a, username, out)
	if err != nil {
		return err
	}
	r, err := a.ResetSecondFactor(ctx, m.ID)
	if err != nil {
		return err
	}
	app := "was off"
	if r.TOTP {
		app = "turned off"
	}
	sayf(out, "Authenticator app %s, %d passkey(s) removed, %d session(s) ended. Sign in with the password.\n", app, r.Passkeys, r.Sessions)
	return nil
}

// askPassword asks twice and again after a refusal, up to maxRounds.
func askPassword(p Prompter, out io.Writer) (string, error) {
	for range maxRounds {
		pw, err := p.Password("Password (at least 12 characters): ")
		if err != nil {
			return "", err
		}
		again, err := p.Password("Password again: ")
		if err != nil {
			return "", err
		}
		switch {
		case pw != again:
			sayln(out, "The passwords don't match.")
		case len([]rune(pw)) < auth.MinPasswordLen:
			sayln(out, "The password must be at least 12 characters.")
		case len(pw) > auth.MaxPasswordLen:
			sayln(out, "The password is too long.")
		default:
			return pw, nil
		}
	}
	return "", errors.New("too many bad passwords")
}

// ttyPrompter reads from the process's terminal.
type ttyPrompter struct {
	in  *os.File
	out io.Writer
	r   *bufio.Reader
}

var errNoTerminal = errors.New("no terminal: run it with docker exec -it proxier /bin/proxier manage <command>")

func newTTYPrompter(in *os.File, out io.Writer) (*ttyPrompter, error) {
	if !term.IsTerminal(int(in.Fd())) {
		return nil, errNoTerminal
	}
	return &ttyPrompter{in: in, out: out, r: bufio.NewReader(in)}, nil
}

func (t *ttyPrompter) Line(prompt string) (string, error) {
	say(t.out, prompt)
	s, err := t.r.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (t *ttyPrompter) Password(prompt string) (string, error) {
	say(t.out, prompt)
	b, err := term.ReadPassword(int(t.in.Fd()))
	sayln(t.out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}
