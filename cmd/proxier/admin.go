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

var errAdminExists = errors.New("an admin already exists; use reset-password")

func createAdmin(ctx context.Context, a *auth.Service, p Prompter, out io.Writer) error {
	if ok, err := a.AdminExists(ctx); err != nil {
		return err
	} else if ok {
		sayln(out, "An admin already exists; use reset-password.")
		return errAdminExists
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
	if err := a.CreateAdmin(ctx, username, pw); err != nil {
		return err
	}
	sayln(out, "Admin created. Sign in at your base URL.")
	return nil
}

func resetPassword(ctx context.Context, a *auth.Service, p Prompter, out io.Writer) error {
	if ok, err := a.AdminExists(ctx); err != nil {
		return err
	} else if !ok {
		sayln(out, "There is no admin yet; use create-admin.")
		return auth.ErrNoAdmin
	}
	pw, err := askPassword(p, out)
	if err != nil {
		return err
	}
	ended, err := a.ResetPassword(ctx, pw)
	if err != nil {
		return err
	}
	sayf(out, "Password changed. %d session(s) ended.\n", ended)
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
