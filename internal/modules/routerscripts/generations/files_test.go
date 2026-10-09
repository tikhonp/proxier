package generations

import "testing"

func TestFileName(t *testing.T) {
	for router, want := range map[string]string{
		"Dacha":      "fresh-router-Dacha-v4.rsc",
		"Дача 2":     "fresh-router-2-v4.rsc",
		"!!!":        "fresh-router-router-v4.rsc",
		"":           "fresh-router-router-v4.rsc",
		"a  b--c.d_": "fresh-router-a-b-c.d_-v4.rsc",
	} {
		if got := FileName("fresh-router", router, 4); got != want {
			t.Errorf("%q: %q, want %q", router, got, want)
		}
	}
}
