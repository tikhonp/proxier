package output_test

import (
	"encoding/base64"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
)

func TestScoreNamesAcrossFormatsAndPreview(t *testing.T) {
	s := nl1()
	s.Endpoints[0].DisplayName += " · main"
	s.Endpoints = append(s.Endpoints, ep("nl-1", "extra", "🇳🇱 Netherlands 1 · extra", 9))
	s.Scores = map[string]map[string]float64{"main": {"ip": 85.5, "claude": 0, "chatgpt": 63}}
	for _, mode := range output.ScoreModes {
		for _, format := range output.Names() {
			for _, masked := range []bool{false, true} {
				r := request(s, de1())
				r.ScoreNames, r.Format, r.Masked = mode, format, masked
				resp := build(t, r)
				body := resp.Body
				if format == "uri-base64" {
					var err error
					body, err = base64.StdEncoding.DecodeString(string(body))
					if err != nil {
						t.Fatal(err)
					}
				}
				if string(body) != strings.Join(resp.Lines, "\n")+"\n" {
					t.Fatal("format changed names")
				}
				for i, line := range resp.Lines {
					_, fragment, _ := strings.Cut(line, "#")
					name, err := url.PathUnescape(fragment)
					if err != nil {
						t.Fatal(err)
					}
					want := []string{"🇳🇱 Netherlands 1 · main", "🇳🇱 Netherlands 1 · extra", "🇩🇪 Germany 1"}[i]
					if i == 0 {
						want += map[string]string{"": "", "ip": " · IP 85.5", "claude": " · Claude 0", "chatgpt": " · ChatGPT 63"}[mode]
					}
					if name != want {
						t.Fatalf("%s %s masked=%t: %q, want %q", mode, format, masked, name, want)
					}
					base, _ := output.URIs(r.Servers, masked)
					gotConnection, _, _ := strings.Cut(line, "#")
					wantConnection, _, _ := strings.Cut(base[i], "#")
					if gotConnection != wantConnection {
						t.Fatal("suffix changed connection parameters")
					}
				}
				if s.Endpoints[0].DisplayName != "🇳🇱 Netherlands 1 · main" {
					t.Fatal("catalog endpoint mutated")
				}
			}
		}
	}
	for _, state := range []string{"disabled", "deleted"} {
		r := request(s)
		r.Link.State = state
		r.ScoreNames = "claude"
		if !reflect.DeepEqual(build(t, r).Lines, func() []string { r.ScoreNames = ""; return build(t, r).Lines }()) {
			t.Fatal("stub decorated")
		}
	}
}
