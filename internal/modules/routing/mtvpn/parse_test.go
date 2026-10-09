package mtvpn_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
)

func TestParseMtvpnFormat(t *testing.T) {
	f, err := mtvpn.Parse(`# mtvpn config
router: 192.168.88.1
services:
  - anthropic
  - "iplist:youtube.com"
  - 'mine=https://example.com/list.txt'

service_lists:
  - https://example.com/mtvpn-main.txt
shadowrocket_base: "https://files.example.com/base.conf"
shadowrocket_password: 'hunter2: secret'
shadowrocket_user: me
empty_key:
`)
	if err != nil {
		t.Fatal(err)
	}
	want := mtvpn.File{
		Services:     []string{"anthropic", "iplist:youtube.com", "mine=https://example.com/list.txt"},
		ServiceLists: []string{"https://example.com/mtvpn-main.txt"},
		Base:         "https://files.example.com/base.conf",
		Ignored:      []string{"router", "shadowrocket_password", "shadowrocket_user", "empty_key"},
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("got %+v", f)
	}
	if strings.Contains(strings.Join(append(append(f.Services, f.ServiceLists...), f.Base), " "), "hunter2") {
		t.Error("a password value is in File")
	}
	// items under an ignored key are dropped with it
	f, err = mtvpn.Parse("routers:\n  - 10.0.0.1 secret\nservices:\n")
	if err != nil || len(f.Services) != 0 || len(f.Ignored) != 1 || f.Ignored[0] != "routers" {
		t.Errorf("empty services: %+v %v", f, err)
	}
	// errors name the line only, never its text
	for text, line := range map[string]int{
		"- anthropic\n": 1,
		"services:\n  - a\nshadowrocket_password hunter2\n": 3,
	} {
		_, err := mtvpn.Parse(text)
		var pe *mtvpn.ParseError
		if !errors.As(err, &pe) || pe.Line != line {
			t.Errorf("%q: %v", text, err)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "anthropic") {
			t.Errorf("the error holds the line's text: %v", err)
		}
	}
	if _, err := mtvpn.Parse("- x\n"); err == nil || err.Error() != "line 1 is a list item without a key" {
		t.Errorf("no key: %v", err)
	}
	if _, err := mtvpn.Parse("a: b\nnot a pair\n"); err == nil || err.Error() != "line 2 is not 'key: value'" {
		t.Errorf("not kv: %v", err)
	}
	if _, err := mtvpn.Parse(strings.Repeat("#", mtvpn.MaxFile+1)); !errors.Is(err, mtvpn.ErrTooBig) {
		t.Errorf("too big: %v", err)
	}
}
