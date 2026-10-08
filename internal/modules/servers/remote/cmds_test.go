package remote_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"mvdan.cc/sh/v3/syntax"
)

func mustHTTP(t *testing.T, url, resolve string) string {
	t.Helper()
	cmd, err := remote.CmdHTTP(url, resolve)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestEveryCommandParsesBack(t *testing.T) {
	dir := "/opt/proxier/my stack" // a space: quoting must hold
	for _, c := range []struct {
		cmd  string
		op   remote.Op
		sudo bool
		args []string
	}{
		{remote.CmdOSRelease(), remote.OpOSRelease, false, nil},
		{remote.CmdArch(), remote.OpArch, false, nil},
		{remote.CmdListeners(), remote.OpListeners, false, nil},
		{remote.CmdEnsureUser(), remote.OpEnsureUser, false, nil},
		{remote.CmdInstallSudoers(), remote.OpInstallSudoer, false, nil},
		{remote.CmdInstallKeys(), remote.OpInstallKeys, false, nil},
		{remote.CmdSudoCheck(), remote.OpSudoCheck, true, nil},
		{remote.CmdSSHDDropin(), remote.OpSSHDDropin, true, nil},
		{remote.CmdSSHDReload(), remote.OpSSHDReload, true, nil},
		{remote.CmdSSHDRollback(), remote.OpSSHDRollback, true, nil},
		{remote.CmdHushlogin(), remote.OpHushlogin, false, nil},
		{remote.CmdApt(), remote.OpApt, true, nil},
		{remote.CmdDockerCheck(), remote.OpDockerCheck, true, nil},
		{remote.CmdDockerInstall(), remote.OpDockerInstall, true, nil},
		{remote.CmdFirewall([]string{"22/tcp", "443/tcp"}), remote.OpFirewall, true, []string{"22/tcp", "443/tcp"}},
		{remote.CmdPrepareDirs([]string{dir, dir + "/nginx"}), remote.OpPrepareDirs, true, []string{dir, dir + "/nginx"}},
		{remote.CmdRemove([]string{dir + "/a.txt", dir + "/it's"}), remote.OpRemove, true, []string{dir + "/a.txt", dir + "/it's"}},
		{remote.CmdRun(dir, "./issue-cert.sh --now"), remote.OpRun, true, []string{dir, "./issue-cert.sh --now"}},
		{remote.CmdComposePull(dir), remote.OpComposePull, true, []string{dir}},
		{remote.CmdComposeUp(dir), remote.OpComposeUp, true, []string{dir}},
		{remote.CmdComposeDown(dir, true), remote.OpComposeDown, true, []string{dir, "true"}},
		{remote.CmdComposeDown(dir, false), remote.OpComposeDown, true, []string{dir, "false"}},
		{remote.CmdComposePS(dir), remote.OpComposePS, true, []string{dir}},
		{remote.CmdComposeRestart(dir), remote.OpComposeRestart, true, []string{dir}},
		{remote.CmdComposeImages(dir), remote.OpComposeImages, true, []string{dir}},
		{remote.CmdComposeLogs(dir, "xray"), remote.OpComposeLogs, true, []string{dir, "xray"}},
		{remote.CmdReboot(), remote.OpReboot, true, nil},
		{remote.CmdBootID(), remote.OpBootID, false, nil},
		{mustHTTP(t, "https://h.example/x?y=1", "127.0.0.1"), remote.OpHTTP, false, []string{"https://h.example/x?y=1", "127.0.0.1"}},
		{mustHTTP(t, "http://h.example/", ""), remote.OpHTTP, false, []string{"http://h.example/", ""}},
		{remote.CmdCertExpiry(dir + "/c.pem"), remote.OpCertExpiry, true, []string{dir + "/c.pem"}},
		{remote.CmdDisk(), remote.OpDisk, false, nil},
	} {
		got, ok := remote.Parse(c.cmd)
		if !ok || got.Op != c.op || got.Sudo != c.sudo || !reflect.DeepEqual(append([]string(nil), got.Args...), append([]string(nil), c.args...)) {
			t.Errorf("Parse(%s) = %+v, %v; want %s sudo=%v args=%q", c.cmd, got, ok, c.op, c.sudo, c.args)
		}
	}
	if _, ok := remote.Parse("rm -rf /"); ok {
		t.Error("an unknown command parsed")
	}
}

// TestScriptsAreValidShell parses the body of every sh -c command with a real
// shell parser: a typo in a script would otherwise only show on a real server.
func TestScriptsAreValidShell(t *testing.T) {
	dir := "/opt/proxier/x"
	for _, cmd := range []string{
		remote.CmdEnsureUser(), remote.CmdInstallSudoers(), remote.CmdInstallKeys(), remote.CmdSSHDDropin(), remote.CmdSSHDReload(),
		remote.CmdSSHDRollback(), remote.CmdApt(), remote.CmdDockerInstall(), remote.CmdFirewall([]string{"22/tcp"}),
		remote.CmdCertExpiry(dir + "/c.pem"), remote.CmdStats(dir), remote.CmdRun(dir, "./a.sh && ./b.sh"),
	} {
		words, err := remote.Split(cmd)
		if err != nil {
			t.Fatal(err)
		}
		i := indexOf(words, "-c")
		if i < 0 || i+1 >= len(words) {
			t.Fatalf("no script in %s", cmd)
		}
		if _, err := syntax.NewParser().Parse(strings.NewReader(words[i+1]), "script"); err != nil {
			t.Errorf("the script of %s… does not parse: %v\n%s", words[i+3:], err, words[i+1])
		}
	}
}

func indexOf(words []string, w string) int {
	for i, x := range words {
		if x == w {
			return i
		}
	}
	return -1
}

func TestHTTPCommandUsesTheURLsPort(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://h.example:8443/", "h.example:8443:127.0.0.1"},
		{"https://h.example/", "h.example:443:127.0.0.1"},
		{"http://h.example/path", "h.example:80:127.0.0.1"},
		{"http://user@h.example:81/", "h.example:81:127.0.0.1"},
	} {
		if cmd := mustHTTP(t, c.url, "127.0.0.1"); !strings.Contains(cmd, "--resolve "+c.want+" ") {
			t.Errorf("%s: %s", c.url, cmd)
		}
	}
	if _, err := remote.CmdHTTP("ftp://x/", "127.0.0.1"); err == nil {
		t.Error("ftp accepted")
	}
}

func TestOSAndArchChecks(t *testing.T) {
	for _, c := range []struct {
		found   string
		allowed []string
		ok      bool
	}{
		{"debian-12", []string{"debian-12", "ubuntu-24.04"}, true},
		{"ubuntu-20.04", []string{"ubuntu-22.04+"}, false},
		{"ubuntu-22.04", []string{"ubuntu-22.04+"}, true},
		{"ubuntu-24.04", []string{"ubuntu-22.04+"}, true},
		{"debian-13", []string{"ubuntu-22.04+"}, false},
		{"ubuntu-9.04", []string{"ubuntu-22.04+"}, false},
		{"alpine-3.20", nil, true},
	} {
		if got := remote.OSAllowed(c.found, c.allowed); got != c.ok {
			t.Errorf("OSAllowed(%q, %q) = %v", c.found, c.allowed, got)
		}
	}
	if got := remote.ParseOS("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n"); got != "debian-12" {
		t.Errorf("ParseOS = %q", got)
	}
	if remote.ParseArch("x86_64\n") != "amd64" || remote.ParseArch("aarch64") != "arm64" || remote.ParseArch("riscv64") != "riscv64" {
		t.Error("ParseArch")
	}
	ls := remote.ParseListeners("LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:((\"nginx\",pid=812,fd=6))\nLISTEN 0 4096 [::]:80 [::]:* \n")
	if len(ls) != 2 || ls[0].Port != 443 || ls[0].Describe() != "nginx (pid 812)" || ls[1].Port != 80 || ls[1].Describe() != "an unknown process" {
		t.Errorf("ParseListeners = %+v", ls)
	}
}
