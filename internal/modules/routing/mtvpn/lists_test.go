package mtvpn_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
)

func TestReadServiceList(t *testing.T) {
	got, err := mtvpn.ReadServiceList(`# mtvpn-main
services:
- v2fly:youtube
  - google   # the search
iplist:instagram.com
https://example.com/a#b.txt
	linkedin	
#netflix
`)
	want := []string{"v2fly:youtube", "google", "iplist:instagram.com", "https://example.com/a#b.txt", "linkedin"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %q %v", got, err)
	}
	_, err = mtvpn.ReadServiceList("youtube\ngoogle maps\n")
	var le *mtvpn.ListLineError
	if !errors.As(err, &le) || le.Line != 2 {
		t.Errorf("space inside: %v", err)
	}
	for _, text := range []string{"", "# only comments\n\nservices:\n"} {
		if _, err := mtvpn.ReadServiceList(text); !errors.Is(err, mtvpn.ErrEmptyList) {
			t.Errorf("%q: %v", text, err)
		}
	}
}
