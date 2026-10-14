package auth

import "time"

// CodeAt is the authenticator app's code for a base32 secret at t.
func CodeAt(secret string, t time.Time) string {
	raw, err := b32.DecodeString(secret)
	if err != nil {
		panic(err)
	}
	return hotp(raw, uint64(totpStep(t)), totpDigits)
}
