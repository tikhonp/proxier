package agent

import (
	"errors"
	"strings"
	"testing"
)

const secretScript = `# PARAMETERS
# admin password
# @secret
:local pass "hunter 2!"
# @secret
:local pin 1234
# @secret
:local empty ""
# @fill subscription-link
:local subUrl "https://proxier.example/s/tok123"
:local lanNet "10.230.1"
:local derived ($pass . "-x")
# END PARAMETERS
:put $pass
`

func TestMaskHidesSecretAndLinkDefaults(t *testing.T) {
	got := maskBody(secretScript)
	for _, leak := range []string{"hunter 2!", "1234", "tok123"} {
		if strings.Contains(got, leak) {
			t.Errorf("masked body holds %q:\n%s", leak, got)
		}
	}
	for _, want := range []string{`:local pass "MASKED-BY-PROXIER"`, `:local pin MASKED-BY-PROXIER`, `:local subUrl "MASKED-BY-PROXIER"`, `:local empty ""`, `:local lanNet "10.230.1"`, ":put $pass\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("masked body lacks %q:\n%s", want, got)
		}
	}
	if plain := "# PARAMETERS\n:local a \"b\"\n# END PARAMETERS\n"; maskBody(plain) != plain {
		t.Error("a script with nothing hidden changed")
	}
}

func TestUnmaskPutsTheDefaultsBack(t *testing.T) {
	edited := strings.Replace(maskBody(secretScript), `:local lanNet "10.230.1"`, `:local lanNet "10.231.1"`, 1)
	got, err := unmask(edited, secretScript)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(secretScript, "10.230.1", "10.231.1", 1); got != want {
		t.Fatalf("unmasked:\n%s\nwant:\n%s", got, want)
	}
	// a secret the agent replaced with a real value keeps that value
	typed := strings.Replace(maskBody(secretScript), `:local pass "MASKED-BY-PROXIER"`, `:local pass "new-one"`, 1)
	if got, err := unmask(typed, secretScript); err != nil || !strings.Contains(got, `:local pass "new-one"`) || !strings.Contains(got, "tok123") {
		t.Fatalf("typed secret: %v\n%s", err, got)
	}
	// the annotation dropped: the stored default still comes back
	dropped := strings.Replace(maskBody(secretScript), "# admin password\n# @secret\n", "# admin password\n", 1)
	if got, err := unmask(dropped, secretScript); err != nil || !strings.Contains(got, `:local pass "hunter 2!"`) {
		t.Fatalf("annotation dropped: %v\n%s", err, got)
	}
}

func TestUnmaskRefusesAMaskWithNothingBehindIt(t *testing.T) {
	for name, body := range map[string]string{
		"renamed":       strings.Replace(maskBody(secretScript), ":local pass ", ":local password ", 1),
		"not a secret":  strings.Replace(maskBody(secretScript), `:local lanNet "10.230.1"`, `:local lanNet "MASKED-BY-PROXIER"`, 1),
		"quotes gone":   strings.Replace(maskBody(secretScript), `:local pass "MASKED-BY-PROXIER"`, `:local pass MASKED-BY-PROXIER`, 1),
		"new parameter": strings.Replace(maskBody(secretScript), "# END PARAMETERS", ":local extra MASKED-BY-PROXIER\n# END PARAMETERS", 1),
	} {
		if _, err := unmask(body, secretScript); !errors.Is(err, ErrMaskLeft) {
			t.Errorf("%s: %v, want ErrMaskLeft", name, err)
		}
	}
}
