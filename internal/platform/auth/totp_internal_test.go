package auth

import (
	"testing"
	"time"
)

func TestHOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		unix int64
		want string
	}{
		{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"},
		{1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		if got := hotp(secret, uint64(totpStep(time.Unix(c.unix, 0))), 8); got != c.want {
			t.Errorf("T=%d: %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestTOTPAcceptsOneStepOfDriftAndNoReuse(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_800_000_000, 0)
	cur := totpStep(now)
	code := func(step int64) string { return hotp(secret, uint64(step), totpDigits) }
	for _, d := range []int64{-1, 0, 1} {
		if step, ok := matchTOTP(secret, code(cur+d), now, 0); !ok || step != cur+d {
			t.Errorf("drift %d: step %d ok %v", d, step, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := matchTOTP(secret, code(cur+d), now, 0); ok {
			t.Errorf("drift %d accepted", d)
		}
	}
	if _, ok := matchTOTP(secret, code(cur), now, cur); ok {
		t.Error("the code of the last used step was accepted again")
	}
	if _, ok := matchTOTP(secret, code(cur-1), now, cur); ok {
		t.Error("a code older than the last used one was accepted")
	}
	if _, ok := matchTOTP(secret, code(cur)[:3]+" "+code(cur)[3:], now, 0); !ok {
		t.Error("a code typed with a space was refused")
	}
	if _, ok := matchTOTP(secret, "", now, 0); ok {
		t.Error("an empty code was accepted")
	}
}

func TestEphemeralIsSingleUseExpiresAndStaysBounded(t *testing.T) {
	now := time.Unix(0, 0)
	e := newEphemeral[int](time.Minute, 3)
	a := e.put(now, 1)
	if v, _, ok := e.take(a, now); !ok || v != 1 {
		t.Fatal("a fresh entry was not found")
	}
	if _, _, ok := e.take(a, now); ok {
		t.Error("an entry was taken twice")
	}
	b := e.put(now, 2)
	if _, _, ok := e.take(b, now.Add(time.Minute)); ok {
		t.Error("an expired entry was taken")
	}
	first := e.put(now, 1)
	for i := range 3 {
		e.put(now.Add(time.Duration(i+1)*time.Second), i)
	}
	if len(e.items) != 3 {
		t.Errorf("%d entries, want the cap of 3", len(e.items))
	}
	if _, _, ok := e.take(first, now); ok {
		t.Error("the oldest entry survived past the cap")
	}
}
