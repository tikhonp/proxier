// Package serverstest gives the servers module's tests a fake VPS and a
// harness around the whole module. The VPS speaks SSH (sshxtest) and answers
// the commands of package remote by parsing them with remote.Parse, so a
// command changed in remote changes here too.
package serverstest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
)

// DefaultRootPassword is what a new VPS's root accepts.
const DefaultRootPassword = "hunter2-root-pw"

// VPS is a fake Debian/Ubuntu server: users, sudoers, an sshd drop-in, apt,
// Docker, ufw, files under the stack directory, compose services, a
// certificate and a disk. It keeps state between commands and connections.
// Set the exported fields before the first connection; they are read under a
// lock only by the VPS's own methods.
type VPS struct {
	*sshxtest.Server
	t testing.TB

	OS, Arch     string         // "debian-12", "amd64"
	PortsInUse   map[int]string // 443 → "nginx (pid 812)"
	Docker       bool
	DiskFreePct  float64
	CertDaysLeft int // <0 expired; the certificate file exists once issued
	// PSArray makes `docker compose ps` print one JSON array instead of one
	// object per line.
	PSArray bool
	// HTTPStatus overrides what a local request to a URL gets; otherwise 200
	// while every compose service runs and 000 (no answer) when none does.
	HTTPStatus map[string]string

	// Faults for edge cases.
	BreakKeyLoginAfterSSHD bool   // key login fails once sshd reloaded with Proxier's drop-in
	BreakAuthorizedKeys    bool   // keys are written but sshd will not read them
	VisudoFails            bool   // visudo -cf rejects the sudoers file
	RequireTTY             bool   // sudo wants a terminal
	ComposeUpFails         string // docker compose up prints this and fails
	CertbotRateLimited     bool   // issue-cert.sh fails with Let's Encrypt's rate limit
	DockerInstallFails     bool
	RebootNeverReturns     bool // the machine does not come back after systemctl reboot

	// RunHooks answer `run` steps by their command ("./issue-cert.sh"); the
	// hook gets the stack directory and returns the exit code.
	RunHooks map[string]func(v *VPS, dir string, stdout, stderr io.Writer) int

	// RebootDowntime is how long logins fail after a reboot (default 30 ms).
	RebootDowntime time.Duration

	mu         sync.Mutex
	images     map[string]string // service → image ID
	newImages  map[string]bool   // services the registry has a newer image of
	imageSeq   int
	reboots    int
	holds      map[remote.Op]*hold
	fails      map[remote.Op]*failure
	rootPass   string
	users      map[string]*user
	sudoers    string
	dropin     string
	ufwAllowed []string
	ufwOn      bool
	aptRuns    int
	services   map[string]string
	commands   []string
	hardened   bool
}

type user struct {
	locked bool
	keys   []string
}

// NewVPS starts a fake server whose root accepts DefaultRootPassword. No key
// is authorized yet; installing access does that.
func NewVPS(t testing.TB) *VPS {
	t.Helper()
	v := &VPS{
		Server: sshxtest.NewServer(t, nil), t: t,
		OS: "debian-12", Arch: "amd64", DiskFreePct: 60, CertDaysLeft: 89,
		PortsInUse: map[int]string{}, HTTPStatus: map[string]string{}, RunHooks: map[string]func(*VPS, string, io.Writer, io.Writer) int{},
		rootPass: DefaultRootPassword, users: map[string]*user{"root": {locked: true}}, services: map[string]string{},
		images: map[string]string{}, newImages: map[string]bool{}, RebootDowntime: 30 * time.Millisecond,
	}
	v.AllowPassword("root", v.rootPass)
	v.HandleFunc(func(string, string) bool { return true }, v.exec)
	return v
}

type failure struct {
	msg   string
	times int // <0: every time
}

// Fail makes commands of op print msg and exit 1, the next times of them
// (every one when times is negative). Fail(op, "", 0) lifts it.
func (v *VPS) Fail(op remote.Op, msg string, times int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fails == nil {
		v.fails = map[remote.Op]*failure{}
	}
	if times == 0 {
		delete(v.fails, op)
		return
	}
	v.fails[op] = &failure{msg: msg, times: times}
}

type hold struct {
	reached chan struct{}
	gate    chan struct{}
	once    sync.Once
}

// Hold makes commands of op wait until release is called: reached is closed
// when the first one arrives. A test uses it to stop the job in the middle of
// a step. release also lets the commands that wait go on.
func (v *VPS) Hold(op remote.Op) (reached <-chan struct{}, release func()) {
	h := &hold{reached: make(chan struct{}), gate: make(chan struct{})}
	v.mu.Lock()
	if v.holds == nil {
		v.holds = map[remote.Op]*hold{}
	}
	v.holds[op] = h
	v.mu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			v.mu.Lock()
			delete(v.holds, op)
			v.mu.Unlock()
			close(h.gate)
		})
	}
	v.t.Cleanup(release)
	return h.reached, release
}

// Unharden undoes what provisioning did to sshd, so root's password login
// works again: the machine is provisioned a second time, as a test that needs
// several servers on one fake VPS does (each with its own address; see
// Harness.AddServer).
func (v *VPS) Unharden() {
	v.mu.Lock()
	v.dropin = ""
	v.hardened = false
	pw := v.rootPass
	v.mu.Unlock()
	v.AllowPassword("root", pw)
	v.BlockLogins("root", false)
}

// SetRootPassword changes what root's password login accepts.
func (v *VPS) SetRootPassword(pw string) {
	v.mu.Lock()
	v.rootPass = pw
	v.mu.Unlock()
	v.AllowPassword("root", pw)
}

// AddUser makes an account exist already, with these authorized keys (parsed
// from authorized_keys lines); an image or an earlier attempt would have done it.
func (v *VPS) AddUser(name string, keys ...string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.users[name] = &user{locked: true, keys: append([]string(nil), keys...)}
}

// AuthorizedKeys is the user's authorized_keys, line by line.
func (v *VPS) AuthorizedKeys(name string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if u := v.users[name]; u != nil {
		return append([]string(nil), u.keys...)
	}
	return nil
}

// HasUser reports whether the account exists, and whether its password is locked.
func (v *VPS) HasUser(name string) (exists, locked bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	u := v.users[name]
	if u == nil {
		return false, false
	}
	return true, u.locked
}

// Sudoers is /etc/sudoers.d/proxier, "" when absent.
func (v *VPS) Sudoers() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sudoers
}

// SetSudoers puts a sudoers file in place (an earlier attempt's).
func (v *VPS) SetSudoers(s string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sudoers = s
}

// SSHDDropin is the hardening drop-in, "" when absent.
func (v *VPS) SSHDDropin() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.dropin
}

// Hardened reports whether sshd was reloaded with the drop-in.
func (v *VPS) Hardened() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hardened
}

// Firewall is what ufw allows and whether it is on.
func (v *VPS) Firewall() (allowed []string, on bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.ufwAllowed...), v.ufwOn
}

// AptRuns counts apt installs; Commands lists every command seen as
// "user: command", in order.
func (v *VPS) AptRuns() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.aptRuns
}

func (v *VPS) Commands() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.commands...)
}

// SetService sets a compose service's state ("running", "exited").
func (v *VPS) SetService(name, state string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.services[name] = state
}

// Services returns the compose services and their states.
func (v *VPS) Services() map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]string{}
	for k, s := range v.services {
		out[k] = s
	}
	return out
}

// NewImage makes the next `docker compose pull` bring a different image for
// service, as a registry that published a new build would.
func (v *VPS) NewImage(service string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.newImages[service] = true
}

// Reboots counts how many times the machine was asked to reboot.
func (v *VPS) Reboots() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.reboots
}

// AnyFileContains reports whether some file on the server holds s: the fake
// proxy follows what the stack on the server accepts.
func (v *VPS) AnyFileContains(s string) bool {
	found := false
	_ = filepath.WalkDir(v.Dir(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(s)) {
			found = true
		}
		return nil
	})
	return found
}

// real maps a path of the server to the host's.
func (v *VPS) real(p string) string {
	return filepath.Join(v.Dir(), filepath.FromSlash(filepath.Clean("/"+p)))
}

// File returns what was uploaded (or written) at path.
func (v *VPS) File(path string) ([]byte, bool) {
	b, err := os.ReadFile(v.real(path))
	return b, err == nil
}

// PutFile writes a file on the server, creating its directories.
func (v *VPS) PutFile(path string, data []byte) {
	v.t.Helper()
	p := v.real(path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		v.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		v.t.Fatal(err)
	}
}

// Files lists the files under dir, relative to it, sorted.
func (v *VPS) Files(dir string) []string {
	root := v.real(dir)
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// exec answers one command.
func (v *VPS) exec(s *sshxtest.Session) int {
	v.mu.Lock()
	v.commands = append(v.commands, s.User+": "+s.Command)
	v.mu.Unlock()
	call, ok := remote.Parse(s.Command)
	if !ok {
		say(s.Stderr, "serverstest: not a command of package remote: %s\n", s.Command)
		return 127
	}
	v.mu.Lock()
	h := v.holds[call.Op]
	v.mu.Unlock()
	if h != nil {
		h.once.Do(func() { close(h.reached) })
		<-h.gate
	}
	v.mu.Lock()
	if f := v.fails[call.Op]; f != nil {
		msg := f.msg
		if f.times > 0 {
			if f.times--; f.times == 0 {
				delete(v.fails, call.Op)
			}
		}
		v.mu.Unlock()
		sayln(s.Stderr, msg)
		return 1
	}
	v.mu.Unlock()
	root := s.User == "root"
	if call.Sudo && !root {
		v.mu.Lock()
		has, tty := v.sudoers != "", v.RequireTTY
		v.mu.Unlock()
		if tty {
			sayln(s.Stderr, "sudo: a terminal is required to read the password; either use the -S option to read from standard input or configure an askpass helper")
			return 1
		}
		if !has {
			sayln(s.Stderr, "sudo: a password is required")
			return 1
		}
	}
	privileged := root || call.Sudo
	needRoot := func() bool {
		if privileged {
			return true
		}
		sayln(s.Stderr, "Permission denied")
		return false
	}
	switch call.Op {
	case remote.OpOSRelease:
		id, ver, _ := strings.Cut(v.OS, "-")
		say(s.Stdout, "PRETTY_NAME=\"%s %s\"\nNAME=\"%s\"\nVERSION_ID=\"%s\"\nID=%s\n", id, ver, id, ver, id)
	case remote.OpArch:
		switch v.Arch {
		case "arm64":
			sayln(s.Stdout, "aarch64")
		case "amd64":
			sayln(s.Stdout, "x86_64")
		default:
			sayln(s.Stdout, v.Arch)
		}
	case remote.OpBootID:
		v.mu.Lock()
		n := v.reboots
		v.mu.Unlock()
		sayln(s.Stdout, fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
	case remote.OpListeners:
		ports := make([]int, 0, len(v.PortsInUse))
		for p := range v.PortsInUse {
			ports = append(ports, p)
		}
		sort.Ints(ports)
		for _, p := range ports {
			name, pid := splitProcess(v.PortsInUse[p])
			say(s.Stdout, "LISTEN 0      511          0.0.0.0:%d        0.0.0.0:*    users:((\"%s\",pid=%d,fd=6))\n", p, name, pid)
		}
	case remote.OpEnsureUser:
		if !needRoot() {
			return 1
		}
		v.mu.Lock()
		if v.users[remote.DeployUser] == nil {
			v.users[remote.DeployUser] = &user{locked: true}
		}
		v.mu.Unlock()
	case remote.OpInstallSudoer:
		if !needRoot() {
			return 1
		}
		body, _ := io.ReadAll(s.Stdin)
		line := strings.TrimSpace(string(body))
		if v.VisudoFails || !regexp.MustCompile(`^[a-z_][a-z0-9_-]* ALL=\(ALL\) NOPASSWD:ALL$`).MatchString(line) {
			sayln(s.Stderr, "visudo: >>> /tmp/tmp.x: syntax error near line 1 <<<")
			return 1
		}
		v.mu.Lock()
		v.sudoers = string(body)
		v.mu.Unlock()
	case remote.OpInstallKeys:
		if !needRoot() {
			return 1
		}
		body, _ := io.ReadAll(s.Stdin)
		v.installKeys(string(body))
	case remote.OpSudoCheck:
		// the sudo gate above is the whole check
	case remote.OpSSHDDropin:
		if !needRoot() {
			return 1
		}
		body, _ := io.ReadAll(s.Stdin)
		v.mu.Lock()
		v.dropin = string(body)
		v.mu.Unlock()
	case remote.OpSSHDReload:
		v.reloadSSHD()
	case remote.OpSSHDRollback:
		v.mu.Lock()
		v.dropin = ""
		v.mu.Unlock()
		v.reloadSSHD()
	case remote.OpHushlogin:
	case remote.OpApt:
		v.mu.Lock()
		v.aptRuns++
		v.mu.Unlock()
	case remote.OpDockerCheck:
		if !v.Docker {
			sayln(s.Stderr, "docker: 'compose' is not a docker command.")
			return 1
		}
	case remote.OpDockerInstall:
		if v.DockerInstallFails {
			sayln(s.Stderr, "curl: (6) Could not resolve host: get.docker.com")
			return 6
		}
		v.Docker = true
	case remote.OpFirewall:
		v.mu.Lock()
		v.ufwAllowed, v.ufwOn = append([]string(nil), call.Args...), true
		v.mu.Unlock()
	case remote.OpPrepareDirs:
		for _, d := range call.Args {
			if err := os.MkdirAll(v.real(d), 0o755); err != nil {
				sayln(s.Stderr, err)
				return 1
			}
		}
	case remote.OpRemove:
		for _, p := range call.Args {
			_ = os.Remove(v.real(p))
		}
	case remote.OpRun:
		return v.run(call.Args[0], call.Args[1], s)
	case remote.OpComposePull:
		v.mu.Lock()
		for svc := range v.newImages {
			v.imageSeq++
			v.images[svc] = fmt.Sprintf("sha256:%064d", v.imageSeq)
		}
		v.newImages = map[string]bool{}
		v.mu.Unlock()
	case remote.OpComposeRestart:
		v.mu.Lock()
		n := len(v.services)
		for name := range v.services {
			v.services[name] = "running"
		}
		v.mu.Unlock()
		if n == 0 {
			sayln(s.Stderr, "no container to restart")
			return 1
		}
	case remote.OpComposeImages:
		v.composeImages(s.Stdout)
	case remote.OpComposeLogs:
		for i := 1; i <= 200; i++ {
			say(s.Stdout, "%s-1  | log line %d of %s\n", call.Args[1], i, call.Args[1])
		}
	case remote.OpReboot:
		v.reboot()
	case remote.OpComposeUp:
		return v.composeUp(call.Args[0], s)
	case remote.OpComposeDown:
		v.mu.Lock()
		v.services = map[string]string{}
		v.mu.Unlock()
	case remote.OpComposePS:
		v.composePS(s.Stdout)
	case remote.OpHTTP:
		sayln(s.Stdout, v.httpStatus(call.Args[0]))
	case remote.OpCertExpiry:
		return v.certExpiry(call.Args[0], s)
	case remote.OpDisk:
		const total = 10_000_000
		avail := int(total * v.DiskFreePct / 100)
		say(s.Stdout, "Filesystem     1024-blocks    Used Available Capacity Mounted on\n/dev/vda1         %d %d %d %d%% /\n",
			total, total-avail, avail, 100-int(v.DiskFreePct))
	default:
		say(s.Stderr, "serverstest: no behaviour for %s\n", call.Op)
		return 127
	}
	return 0
}

func splitProcess(s string) (string, int) {
	m := regexp.MustCompile(`^(\S+)(?: \(pid (\d+)\))?$`).FindStringSubmatch(s)
	if m == nil {
		return s, 1
	}
	pid, _ := strconv.Atoi(m[2])
	if pid == 0 {
		pid = 1
	}
	return m[1], pid
}

// installKeys appends the missing key lines to the deploy user's file and, unless
// the permissions are "wrong", lets sshd accept them.
func (v *VPS) installKeys(body string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	u := v.users[remote.DeployUser]
	if u == nil {
		return
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		have := false
		for _, k := range u.keys {
			if k == line {
				have = true
			}
		}
		if have {
			continue
		}
		u.keys = append(u.keys, line)
		if v.BreakAuthorizedKeys {
			continue // the file has it; sshd refuses to read the file
		}
		if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil {
			v.AuthorizeKey(remote.DeployUser, key)
		}
	}
}

// reloadSSHD applies the drop-in as sshd would: no password login, no root
// login; with BreakKeyLoginAfterSSHD the deploy user's key login breaks too.
// Without the drop-in everything is as it was.
func (v *VPS) reloadSSHD() {
	v.mu.Lock()
	has := v.dropin != ""
	v.hardened = has
	pw := v.rootPass
	v.mu.Unlock()
	if has {
		v.AllowPassword("root", "")
		v.BlockLogins("root", true)
		v.BlockLogins(remote.DeployUser, v.BreakKeyLoginAfterSSHD)
		return
	}
	v.AllowPassword("root", pw)
	v.BlockLogins("root", false)
	v.BlockLogins(remote.DeployUser, false)
}

func (v *VPS) run(dir, command string, s *sshxtest.Session) int {
	if hook := v.RunHooks[command]; hook != nil {
		return hook(v, dir, s.Stdout, s.Stderr)
	}
	if command == "./issue-cert.sh" {
		return v.issueCert(dir, s)
	}
	return 0
}

var domainLine = regexp.MustCompile(`DOMAIN='([^']+)'`)

// issueCert is what the seed's issue-cert.sh does, as far as the stack can tell.
func (v *VPS) issueCert(dir string, s *sshxtest.Session) int {
	script, ok := v.File(dir + "/issue-cert.sh")
	if !ok {
		sayln(s.Stderr, "./issue-cert.sh: No such file or directory")
		return 127
	}
	m := domainLine.FindSubmatch(script)
	if m == nil {
		sayln(s.Stderr, "issue-cert.sh: no DOMAIN")
		return 1
	}
	domain := string(m[1])
	cert := dir + "/certbot/conf/live/" + domain + "/fullchain.pem"
	if _, ok := v.File(cert); ok {
		say(s.Stdout, "A certificate for %s already exists.\n", domain)
		return 0
	}
	say(s.Stdout, "Issuing Let's Encrypt certificate for %s...\n", domain)
	if v.CertbotRateLimited {
		sayln(s.Stderr, "An unexpected error occurred:\nThere were too many requests of a given type :: Error creating new order :: too many certificates (5) already issued for this exact set of identifiers in the last 168 hours")
		say(s.Stdout, "Failed to issue the certificate. Check that the A record of %s points to this server and port 80 is open and free.\n", domain)
		return 1
	}
	v.PutFile(cert, []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"))
	return 0
}

func (v *VPS) composeUp(dir string, s *sshxtest.Session) int {
	if v.ComposeUpFails != "" {
		sayln(s.Stderr, v.ComposeUpFails)
		return 1
	}
	raw, ok := v.File(dir + "/compose.yaml")
	if !ok {
		sayln(s.Stderr, "no configuration file provided: not found")
		return 1
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		sayln(s.Stderr, "yaml: "+err.Error())
		return 1
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for name := range doc.Services {
		if v.services[name] == "" || v.services[name] == "exited" {
			v.services[name] = "running"
		}
		if v.images[name] == "" {
			v.imageSeq++
			v.images[name] = fmt.Sprintf("sha256:%064d", v.imageSeq)
		}
	}
	return 0
}

func (v *VPS) composeImages(w io.Writer) {
	v.mu.Lock()
	names := make([]string, 0, len(v.services))
	for n := range v.services {
		names = append(names, n)
	}
	sort.Strings(names)
	type item struct{ ContainerName, Repository, Tag, ID string }
	var items []item
	for _, n := range names {
		items = append(items, item{ContainerName: "stack-" + n + "-1", Repository: n, Tag: "latest", ID: v.images[n]})
	}
	array := v.PSArray
	v.mu.Unlock()
	if array {
		b, _ := json.Marshal(items)
		sayln(w, string(b))
		return
	}
	for _, it := range items {
		b, _ := json.Marshal(it)
		sayln(w, string(b))
	}
}

// reboot takes logins away for RebootDowntime, or for good.
func (v *VPS) reboot() {
	v.mu.Lock()
	v.reboots++
	down, never := v.RebootDowntime, v.RebootNeverReturns
	v.mu.Unlock()
	v.BlockLogins(remote.DeployUser, true)
	if never {
		return
	}
	timer := time.AfterFunc(down, func() { v.BlockLogins(remote.DeployUser, v.hardenedBreak()) })
	v.t.Cleanup(func() { timer.Stop() })
}

// hardenedBreak is whether the deploy user's key login is meant to be broken.
func (v *VPS) hardenedBreak() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hardened && v.BreakKeyLoginAfterSSHD
}

func (v *VPS) composePS(w io.Writer) {
	v.mu.Lock()
	names := make([]string, 0, len(v.services))
	for n := range v.services {
		names = append(names, n)
	}
	sort.Strings(names)
	type item struct{ Service, Name, State, Health string }
	var items []item
	for _, n := range names {
		items = append(items, item{Service: n, Name: "stack-" + n + "-1", State: v.services[n]})
	}
	array := v.PSArray
	v.mu.Unlock()
	if array {
		b, _ := json.Marshal(items)
		sayln(w, string(b))
		return
	}
	for _, it := range items {
		b, _ := json.Marshal(it)
		sayln(w, string(b))
	}
}

func (v *VPS) httpStatus(url string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if st, ok := v.HTTPStatus[url]; ok {
		return st
	}
	if len(v.services) == 0 {
		return "000"
	}
	for _, st := range v.services {
		if st != "running" {
			return "000"
		}
	}
	return "200"
}

func (v *VPS) certExpiry(path string, s *sshxtest.Session) int {
	if b, ok := v.File(path); !ok || len(bytes.TrimSpace(b)) == 0 {
		sayln(s.Stderr, "missing")
		return 3
	}
	end := time.Now().UTC().Add(time.Duration(v.CertDaysLeft)*24*time.Hour + 12*time.Hour)
	say(s.Stdout, "notAfter=%s\n", end.Format("Jan _2 15:04:05 2006 GMT"))
	return 0
}

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
func sayln(w io.Writer, args ...any)              { _, _ = fmt.Fprintln(w, args...) }
