// Package gen creates the generated values of a template (docs/modules/servers.md#manifest):
// the secrets and identifiers that are made once per server and kept.
package gen

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Bounds of the length parameters, checked by manifest too.
const (
	MinHex, MaxHex           = 4, 128
	MinPassword, MaxPassword = 8, 128
	MinBytes, MaxBytes       = 8, 96
)

// New makes one value of g's kind with its prefix.
func New(g manifest.Generated) (string, error) {
	v, err := value(g)
	if err != nil {
		return "", fmt.Errorf("generated %q: %w", g.Key, err)
	}
	return g.Prefix + v, nil
}

func value(g manifest.Generated) (string, error) {
	switch g.Kind {
	case "uuid":
		return uuid.NewString(), nil
	case "hex":
		if g.Length < MinHex || g.Length > MaxHex {
			return "", fmt.Errorf("length must be %d–%d", MinHex, MaxHex)
		}
		b := make([]byte, (g.Length+1)/2)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b)[:g.Length], nil
	case "base64":
		if g.Bytes < MinBytes || g.Bytes > MaxBytes {
			return "", fmt.Errorf("bytes must be %d–%d", MinBytes, MaxBytes)
		}
		b := make([]byte, g.Bytes)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(b), nil
	case "password":
		if g.Length < MinPassword || g.Length > MaxPassword {
			return "", fmt.Errorf("length must be %d–%d", MinPassword, MaxPassword)
		}
		out := make([]byte, g.Length)
		max := big.NewInt(int64(len(alnum)))
		for i := range out {
			// rand.Int rejects out-of-range draws, so every character is uniform.
			n, err := rand.Int(rand.Reader, max)
			if err != nil {
				return "", err
			}
			out[i] = alnum[n.Int64()]
		}
		return string(out), nil
	}
	return "", fmt.Errorf("unknown kind %q", g.Kind)
}

// Ensure returns a value for every declared key, creating the missing ones.
// Existing values are returned unchanged, and so are values no longer
// declared (a rollback may still need them). created lists the keys made now.
func Ensure(decl []manifest.Generated, have map[string]string) (all map[string]string, created []string, err error) {
	all = make(map[string]string, len(have)+len(decl))
	for k, v := range have {
		all[k] = v
	}
	for _, g := range decl {
		if _, ok := all[g.Key]; ok {
			continue
		}
		v, err := New(g)
		if err != nil {
			return nil, nil, err
		}
		all[g.Key] = v
		created = append(created, g.Key)
	}
	return all, created, nil
}
