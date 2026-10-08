package output_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
)

func TestHeaders(t *testing.T) {
	expires := time.Date(2026, 12, 1, 21, 0, 0, 0, time.UTC) // the end of 1 Dec 2026 in Moscow
	got := output.Headers("Семья", 12, expires)
	want := []output.Header{
		{"profile-title", "base64:0KHQtdC80YzRjw=="},
		{"profile-update-interval", "12"},
		{"subscription-userinfo", "upload=0; download=0; total=0; expire=1796158799"},
		{"content-disposition", "inline; filename*=UTF-8''%D0%A1%D0%B5%D0%BC%D1%8C%D1%8F.txt"},
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("header %d: %+v, want %+v", i, got[i], want[i])
		}
		if got[i].Name != strings.ToLower(got[i].Name) {
			t.Errorf("%s is not lower case", got[i].Name)
		}
	}
	if h := output.Headers("Family", 24, time.Time{}); h[2].Value != "upload=0; download=0; total=0; expire=0" || h[1].Value != "24" {
		t.Errorf("without expiry: %+v", h)
	}
}
