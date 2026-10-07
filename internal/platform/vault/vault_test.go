package vault

import (
	"bytes"
	"errors"
	"testing"
)

func newVault(t *testing.T, fill byte) *Vault {
	t.Helper()
	v, err := New(bytes.Repeat([]byte{fill}, KeySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func TestRoundTrip(t *testing.T) {
	v := newVault(t, 1)
	for _, pt := range [][]byte{[]byte("bot-token"), {}, bytes.Repeat([]byte("x"), 64<<10)} {
		blob := v.Seal(pt, "setting:telegram.bot_token")
		if len(pt) > 0 && bytes.Contains(blob, pt) {
			t.Fatal("blob contains the plaintext")
		}
		got, err := v.Open(blob, "setting:telegram.bot_token")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("got %q, want %q", got, pt)
		}
	}
	s, err := v.OpenString(v.SealString("hello", "a"), "a")
	if err != nil || s != "hello" {
		t.Fatalf("OpenString = %q, %v", s, err)
	}
}

func TestNonceIsRandom(t *testing.T) {
	v := newVault(t, 1)
	if bytes.Equal(v.Seal([]byte("same"), "a"), v.Seal([]byte("same"), "a")) {
		t.Fatal("two seals of the same plaintext are identical")
	}
}

func TestTamper(t *testing.T) {
	v := newVault(t, 1)
	blob := v.Seal([]byte("secret"), "a")
	for i := range blob {
		bad := bytes.Clone(blob)
		bad[i] ^= 0x01
		if _, err := v.Open(bad, "a"); !errors.Is(err, ErrOpen) {
			t.Fatalf("flipping byte %d: err = %v", i, err)
		}
	}
	for _, bad := range [][]byte{nil, blob[:len(blob)-1], blob[:13]} {
		if _, err := v.Open(bad, "a"); !errors.Is(err, ErrOpen) {
			t.Fatalf("short blob: err = %v", err)
		}
	}
}

func TestWrongKey(t *testing.T) {
	blob := newVault(t, 1).Seal([]byte("secret"), "a")
	if _, err := newVault(t, 2).Open(blob, "a"); !errors.Is(err, ErrOpen) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongPlace(t *testing.T) {
	v := newVault(t, 1)
	blob := v.Seal([]byte("secret"), "setting:a")
	if _, err := v.Open(blob, "setting:b"); !errors.Is(err, ErrOpen) {
		t.Fatalf("err = %v", err)
	}
}

func TestLookup(t *testing.T) {
	a, b := newVault(t, 1), newVault(t, 2)
	if !bytes.Equal(a.Lookup("tok"), a.Lookup("tok")) {
		t.Error("Lookup is not deterministic")
	}
	if bytes.Equal(a.Lookup("tok"), a.Lookup("tok2")) {
		t.Error("different tokens share a lookup hash")
	}
	if bytes.Equal(a.Lookup("tok"), b.Lookup("tok")) {
		t.Error("lookup does not depend on the key")
	}
	if len(a.Lookup("tok")) != 32 {
		t.Error("lookup is not 32 bytes")
	}
}

func TestNewToken(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		tok := NewToken()
		if len(tok) != 43 {
			t.Fatalf("token %q has length %d", tok, len(tok))
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("New with a %d-byte key: no error", n)
		}
	}
}
