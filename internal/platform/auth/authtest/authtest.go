// Package authtest is a software passkey for tests.
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator data flags.
const (
	FlagUP = 0x01 // user present
	FlagUV = 0x04 // user verified
	flagAT = 0x40 // attested credential data follows
)

var b64 = base64.RawURLEncoding

// Authenticator is a software passkey: an ES256 key answering WebAuthn
// prompts as a browser would hand them to the server. Tests change its
// fields to misbehave.
type Authenticator struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	id     []byte
	User   []byte // the user handle; Create sets it
	Count  uint32 // the signature counter
	Flags  byte
	Origin string
	RPID   string
}

// New is a passkey for the site at origin.
func New(t *testing.T, origin string) *Authenticator {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &Authenticator{t: t, key: key, id: id, Flags: FlagUP | FlagUV, Origin: origin, RPID: u.Hostname()}
}

func (a *Authenticator) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return b
}

func (a *Authenticator) authData(extra []byte, flags byte) []byte {
	h := sha256.Sum256([]byte(a.RPID))
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, a.Count)
	return append(out, extra...)
}

// Create answers navigator.credentials.create's options.
func (a *Authenticator) Create(options []byte) []byte {
	a.t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		a.t.Fatal(err)
	}
	user, err := b64.DecodeString(o.PublicKey.User.ID)
	if err != nil {
		a.t.Fatal(err)
	}
	a.User = user
	x, y := make([]byte, 32), make([]byte, 32)
	a.key.X.FillBytes(x)
	a.key.Y.FillBytes(y)
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		a.t.Fatal(err)
	}
	cred := make([]byte, 16) // AAGUID: none
	cred = binary.BigEndian.AppendUint16(cred, uint16(len(a.id)))
	cred = append(append(cred, a.id...), cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(cred, a.Flags|flagAT)})
	if err != nil {
		a.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att),
		},
	})
	return body
}

// Get answers navigator.credentials.get's options.
func (a *Authenticator) Get(options []byte) []byte {
	a.t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		a.t.Fatal(err)
	}
	cd := a.clientData("webauthn.get", o.PublicKey.Challenge)
	ad := a.authData(nil, a.Flags)
	h := sha256.Sum256(cd)
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, sha256Sum(append(append([]byte{}, ad...), h[:]...)))
	if err != nil {
		a.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.User),
		},
	})
	return body
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
