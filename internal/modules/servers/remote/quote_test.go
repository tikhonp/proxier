package remote_test

import (
	"os/exec"
	"reflect"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/remote"
)

var nasty = []string{
	"plain", "with space", "it's", `a"b`, "$HOME", "`id`", `back\slash`, "new\nline", "tab\there", "", "'", "''", "a'b'c",
	"*?[x]", "semi;colon && and", "/opt/dir with space/x", "ünï©ode", "-n", "--", "%{http_code}",
}

func TestQuote(t *testing.T) {
	// Split undoes Join.
	got, err := remote.Split(remote.Join(nasty...))
	if err != nil || !reflect.DeepEqual(got, nasty) {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	// And a real shell reads each word back as itself.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, s := range nasty {
		out, err := exec.Command(sh, "-c", "printf %s "+remote.Quote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("sh read %q back as %q (%v)", s, out, err)
		}
	}
	// Safe words stay bare, so the commands stay readable in the log.
	if remote.Quote("/opt/proxier/x-1.yaml") != "/opt/proxier/x-1.yaml" || remote.Quote("a b") != "'a b'" {
		t.Error("quoting is not minimal")
	}
}

func TestSplitRefusesWhatIsNotOurs(t *testing.T) {
	for _, cmd := range []string{`echo "x"`, "a | b", "a && b", "echo $X", "a;b", "'open"} {
		if _, err := remote.Split(cmd); err == nil {
			t.Errorf("Split(%q) accepted a command of another shape", cmd)
		}
	}
}
