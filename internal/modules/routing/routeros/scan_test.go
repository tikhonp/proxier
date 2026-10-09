package routeros

import (
	"strings"
	"testing"
)

func TestScanImport(t *testing.T) {
	for _, line := range []string{
		"syntax error (line 3 column 12)",
		"failure: already have such entry",
		"expected end of command (line 4 column 1)",
		"no such item",
		"input does not match any value of type",
		"bad command name ad (line 1 column 6)",
		"invalid value for argument address",
		"not enough permissions (9)",
	} {
		err := ScanImport(0, []byte("\r\n"+line+"\r\n"+Success+"\r\n"))
		if err == nil || !strings.Contains(err.Error(), line) {
			t.Fatalf("%q: %v", line, err)
		}
	}
	if err := ScanImport(0, []byte("** WARNING: connection is not using a post-quantum key exchange algorithm.\n** This session may be vulnerable to \"store now, decrypt later\" attacks.\n** The server may need to be upgraded. See https://openssh.com/pq.html\n\n"+Success+"\r\n")); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if err := ScanImport(0, nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if err := ScanImport(1, []byte("something odd\n")); err == nil || !strings.Contains(err.Error(), "exit code 1: something odd") {
		t.Fatalf("exit: %v", err)
	}
}
