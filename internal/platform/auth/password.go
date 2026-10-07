package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are the argon2id cost parameters. Memory is in KiB.
type Params struct {
	Memory, Time uint32
	Threads      uint8
}

// DefaultParams is RFC 9106's second recommendation: 64 MiB, 3 passes, 4 lanes.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

const (
	saltLen = 16
	keyLen  = 32
	// A stored string is never trusted to ask for unbounded work.
	maxMemory  = 256 * 1024
	maxTime    = 10
	maxThreads = 16
)

// Hash returns the PHC string of password:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>.
func Hash(p Params, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("auth: malformed password hash")

// Verify reports whether password matches the PHC string. The parameters come
// from the string, so they can change later without a migration.
func Verify(phc, password string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var p Params
	var threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &threads); err != nil {
		return false, errBadHash
	}
	if p.Memory == 0 || p.Memory > maxMemory || p.Time == 0 || p.Time > maxTime || threads == 0 || threads > maxThreads {
		return false, errBadHash
	}
	p.Threads = uint8(threads)
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func checkPassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	if len(pw) > MaxPasswordLen {
		return ErrPasswordTooLong
	}
	return nil
}
