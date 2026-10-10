package country

import "testing"

func TestLabelAndParseRoundTrip(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		for _, c := range Codes() {
			if got := Parse(Label(c, lang)); got != c {
				t.Fatalf("Parse(Label(%s, %s) = %q) = %q", c, lang, Label(c, lang), got)
			}
		}
	}
	if got := Label("NL", "en"); got != "🇳🇱 Netherlands · NL" {
		t.Errorf("Label(NL) = %q", got)
	}
}

func TestParseTakesWhatAPersonTypes(t *testing.T) {
	for in, want := range map[string]string{
		"NL": "NL", "nl": "NL", " se ": "SE", "Netherlands": "NL", "netherlands": "NL",
		"Нидерланды": "NL", "нидерланды": "NL", "🇩🇪 Germany": "DE", "Germany · DE": "DE",
		"": "", "Neth": "", "XX": "", "Atlantis": "", "· ZZ": "", "nld": "",
	} {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrefixIsTheLowerCaseCode(t *testing.T) {
	if Prefix("NL") != "nl" || Prefix("GB") != "gb" {
		t.Error("Prefix")
	}
}

func TestWithdrawnCodesAreNotCountries(t *testing.T) {
	for _, c := range []string{"UK", "DD", "SU", "YU", "AN", "FX", "ZR"} {
		if Valid(c) {
			t.Errorf("%s is valid", c)
		}
	}
	seen := map[string]string{}
	for _, e := range List("en") {
		if other, ok := seen[e.Name]; ok {
			t.Errorf("%s and %s are both %q", other, e.Code, e.Name)
		}
		seen[e.Name] = e.Code
	}
	if len(seen) < 240 {
		t.Errorf("only %d countries", len(seen))
	}
}
