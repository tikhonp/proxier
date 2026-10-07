package remote

import (
	"errors"
	"strings"
)

// Op names a command, for Parse.
type Op string

const (
	OpOSRelease      Op = "os-release"
	OpArch           Op = "arch"
	OpListeners      Op = "listeners"
	OpEnsureUser     Op = "proxier-ensure-user"
	OpInstallSudoer  Op = "proxier-install-sudoers"
	OpInstallKeys    Op = "proxier-install-keys"
	OpSudoCheck      Op = "sudo-check"
	OpSSHDDropin     Op = "proxier-sshd-dropin"
	OpSSHDReload     Op = "proxier-sshd-reload"
	OpSSHDRollback   Op = "proxier-sshd-rollback"
	OpHushlogin      Op = "hushlogin"
	OpApt            Op = "proxier-apt"
	OpDockerCheck    Op = "docker-check"
	OpDockerInstall  Op = "proxier-install-docker"
	OpFirewall       Op = "proxier-ufw"
	OpPrepareDirs    Op = "prepare-dirs"
	OpRemove         Op = "remove"
	OpRun            Op = "run"
	OpComposePull    Op = "compose-pull"
	OpComposeUp      Op = "compose-up"
	OpComposeDown    Op = "compose-down"
	OpComposePS      Op = "compose-ps"
	OpComposeRestart Op = "compose-restart"
	OpComposeImages  Op = "compose-images"
	OpComposeLogs    Op = "compose-logs"
	OpReboot         Op = "reboot"
	OpBootID         Op = "boot-id"
	OpHTTP           Op = "http"
	OpCertExpiry     Op = "proxier-cert-expiry"
	OpDisk           Op = "disk"
)

// DeployUser is the account Proxier works as after the first login.
const DeployUser = "proxier"

// SudoersLine is what /etc/sudoers.d/proxier holds.
const SudoersLine = DeployUser + " ALL=(ALL) NOPASSWD:ALL\n"

// SSHDDropinPath and its content turn password and root login off.
const SSHDDropinPath = "/etc/ssh/sshd_config.d/00-proxier.conf"

const SSHDDropin = "PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\n"

// Call is a parsed command: what it does and with which variable parts.
type Call struct {
	Op   Op
	Args []string
	// Sudo: the command ran as `sudo -n …`.
	Sudo bool
}

// script builds `[sudo -n ]sh -c <script> <tag> <args…>`: the tag names the
// command for Parse, the arguments reach the script as "$@".
func script(sudo bool, body string, tag Op, args ...string) string {
	words := []string{"sh", "-c", body, string(tag)}
	words = append(words, args...)
	if sudo {
		words = append([]string{"sudo", "-n"}, words...)
	}
	return Join(words...)
}

func plain(sudo bool, words ...string) string {
	if sudo {
		words = append([]string{"sudo", "-n"}, words...)
	}
	return Join(words...)
}

// --- facts, as root before the deploy user exists

func CmdOSRelease() string { return "cat /etc/os-release" }
func CmdArch() string      { return "uname -m" }
func CmdListeners() string { return "ss -ltnpH" }

// --- install access, as root

// CmdEnsureUser creates the deploy user with a locked password unless it
// exists; an existing one is left as it is.
func CmdEnsureUser() string {
	return script(false, `if ! id -u `+DeployUser+` >/dev/null 2>&1; then useradd -m -s /bin/bash `+DeployUser+` && passwd -l `+DeployUser+` >/dev/null; fi`, OpEnsureUser)
}

// CmdInstallSudoers reads the sudoers line from stdin and installs it only
// when visudo accepts it; the existing configuration is untouched otherwise.
// A minimal image may not have sudo at all: it is installed first, as root,
// because nothing else can run until the deploy user has it.
func CmdInstallSudoers() string {
	return script(false, `set -e; if ! command -v visudo >/dev/null 2>&1; then export DEBIAN_FRONTEND=noninteractive; apt-get `+aptLock+` update && apt-get `+aptLock+` install -y sudo; fi; tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT; cat > "$tmp"; visudo -cf "$tmp"; install -m 0440 -o root -g root "$tmp" /etc/sudoers.d/`+DeployUser, OpInstallSudoer)
}

// CmdInstallKeys reads public key lines from stdin and appends those the
// deploy user's authorized_keys lacks, with the permissions sshd insists on.
func CmdInstallKeys() string {
	return script(false, `set -e; home=$(getent passwd `+DeployUser+` | cut -d: -f6); install -d -m 700 -o `+DeployUser+` -g `+DeployUser+` "$home/.ssh"; f="$home/.ssh/authorized_keys"; touch "$f"; `+
		`if [ -s "$f" ] && [ -n "$(tail -c1 "$f")" ]; then echo >> "$f"; fi; `+
		`while IFS= read -r line; do [ -n "$line" ] || continue; grep -qxF -- "$line" "$f" || printf '%s\n' "$line" >> "$f"; done; `+
		`chown `+DeployUser+`:`+DeployUser+` "$f"; chmod 600 "$f"`, OpInstallKeys)
}

// CmdSudoCheck proves passwordless sudo works.
func CmdSudoCheck() string { return plain(true, "true") }

// --- base bootstrap, as the deploy user

// CmdSSHDDropin writes the hardening drop-in from stdin.
func CmdSSHDDropin() string {
	return script(true, `install -d -m 755 /etc/ssh/sshd_config.d && cat > `+SSHDDropinPath, OpSSHDDropin)
}

// CmdSSHDReload checks the configuration and reloads sshd.
func CmdSSHDReload() string {
	return script(true, `/usr/sbin/sshd -t && { systemctl reload ssh || systemctl reload sshd; }`, OpSSHDReload)
}

// CmdSSHDRollback removes the drop-in and reloads sshd.
func CmdSSHDRollback() string {
	return script(true, `rm -f `+SSHDDropinPath+`; systemctl reload ssh || systemctl reload sshd`, OpSSHDRollback)
}

// CmdHushlogin silences the login banner for the deploy user (the working
// directory of an SSH command is the home).
func CmdHushlogin() string { return plain(false, "touch", ".hushlogin") }

// aptLock makes apt wait for the lock instead of failing: a fresh VPS is often
// still running its first-boot updates.
const aptLock = "-o DPkg::Lock::Timeout=300"

// CmdApt installs the prerequisites the steps rely on.
func CmdApt() string {
	return script(true, `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a; apt-get `+aptLock+` update && apt-get `+aptLock+` install -y curl ca-certificates openssl ufw`, OpApt)
}

// CmdDockerCheck succeeds when Docker with the compose plugin is installed.
func CmdDockerCheck() string { return plain(true, "docker", "compose", "version") }

// CmdDockerInstall installs Docker with the vendor's script.
func CmdDockerInstall() string {
	return script(true, `curl -fsSL https://get.docker.com | sh`, OpDockerInstall)
}

// CmdFirewall allows each of ports ("22/tcp") and turns ufw on. Docker's
// published ports bypass ufw; they are allowed anyway so the rule set says
// what is meant.
func CmdFirewall(ports []string) string {
	return script(true, `set -e; for p in "$@"; do ufw allow "$p"; done; ufw --force enable`, OpFirewall, ports...)
}

// --- files

// CmdPrepareDirs creates directories owned by the deploy user.
func CmdPrepareDirs(dirs []string) string {
	words := append([]string{"install", "-d", "-o", DeployUser, "-g", DeployUser, "-m", "755", "--"}, dirs...)
	return plain(true, words...)
}

// CmdRemove deletes files Proxier put there and the new version no longer has.
func CmdRemove(paths []string) string {
	return plain(true, append([]string{"rm", "-f", "--"}, paths...)...)
}

// --- template steps

// CmdRun runs command in dir as root.
func CmdRun(dir, command string) string {
	return plain(true, "sh", "-c", "cd "+Quote(dir)+" && "+command)
}

// CmdComposePull pulls the images of the stack in dir.
func CmdComposePull(dir string) string {
	return plain(true, "docker", "compose", "--project-directory", dir, "pull")
}

// CmdComposeUp starts the stack in dir.
func CmdComposeUp(dir string) string {
	return plain(true, "docker", "compose", "--project-directory", dir, "up", "-d", "--remove-orphans")
}

// CmdComposeDown stops the stack, with its volumes when asked.
func CmdComposeDown(dir string, volumes bool) string {
	words := []string{"docker", "compose", "--project-directory", dir, "down"}
	if volumes {
		words = append(words, "-v")
	}
	return plain(true, words...)
}

// CmdComposePS lists the stack's containers as JSON.
func CmdComposePS(dir string) string {
	return plain(true, "docker", "compose", "--project-directory", dir, "ps", "--all", "--format", "json")
}

// CmdComposeRestart restarts the stack's containers in place.
func CmdComposeRestart(dir string) string {
	return plain(true, "docker", "compose", "--project-directory", dir, "restart")
}

// CmdComposeImages lists the images of the stack's containers as JSON.
func CmdComposeImages(dir string) string {
	return plain(true, "docker", "compose", "--project-directory", dir, "images", "--format", "json")
}

// CmdComposeLogs prints the last 200 lines of one service.
func CmdComposeLogs(dir, service string) string {
	return plain(true, "docker", "compose", "--project-directory", dir, "logs", "--no-color", "--tail", "200", service)
}

// CmdReboot restarts the machine. The connection drops while it runs, so the
// caller expects no answer.
func CmdReboot() string { return plain(true, "systemctl", "reboot") }

// CmdBootID prints the id of the current boot: it differs after a reboot, so
// a reconnect can tell the machine that came back from the one that has not
// gone down yet.
func CmdBootID() string { return "cat /proc/sys/kernel/random/boot_id" }

// CmdHTTP requests url once on the server and prints the status code. With
// resolve set ("127.0.0.1"), the request goes there instead of to the host in
// the URL (curl --resolve), whatever DNS says; the port is the URL's, else 443
// for https and 80 for http.
func CmdHTTP(url, resolve string) (string, error) {
	words := []string{"curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "10"}
	if resolve != "" {
		host, port, err := hostPort(url)
		if err != nil {
			return "", err
		}
		words = append(words, "--resolve", host+":"+port+":"+resolve)
	}
	return plain(false, append(words, url)...), nil
}

// hostPort is the host and port a URL connects to.
func hostPort(raw string) (host, port string, err error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		return "", "", errors.New("remote: only http and https URLs: " + raw)
	}
	authority, _, _ := strings.Cut(rest, "/")
	authority, _, _ = strings.Cut(authority, "?")
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	host, port, hasPort := strings.Cut(authority, ":")
	if host == "" {
		return "", "", errors.New("remote: no host in " + raw)
	}
	if !hasPort {
		port = "80"
		if scheme == "https" {
			port = "443"
		}
	}
	return host, port, nil
}

// --- checks

// CmdCertExpiry prints the certificate's end date; exit 3 when the file is
// missing or empty.
func CmdCertExpiry(path string) string {
	return script(true, `test -s "$1" || { echo missing >&2; exit 3; }; openssl x509 -enddate -noout -in "$1"`, OpCertExpiry, path)
}

// CmdDisk prints the root file system's usage in POSIX format.
func CmdDisk() string { return plain(false, "df", "-P", "/") }

// Parse is the inverse of the builders, for serverstest.VPS: which command
// this is, and its variable parts. ok is false for anything else.
func Parse(cmd string) (Call, bool) {
	words, err := Split(cmd)
	if err != nil || len(words) == 0 {
		return Call{}, false
	}
	c := Call{}
	if len(words) >= 2 && words[0] == "sudo" && words[1] == "-n" {
		c.Sudo, words = true, words[2:]
	}
	if len(words) == 0 {
		return Call{}, false
	}
	is := func(prefix ...string) bool {
		if len(words) < len(prefix) {
			return false
		}
		for i, p := range prefix {
			if words[i] != p {
				return false
			}
		}
		return true
	}
	set := func(op Op, args ...string) (Call, bool) { c.Op, c.Args = op, args; return c, true }

	switch {
	case !c.Sudo && is("cat", "/etc/os-release") && len(words) == 2:
		return set(OpOSRelease)
	case !c.Sudo && is("uname", "-m") && len(words) == 2:
		return set(OpArch)
	case !c.Sudo && is("cat", "/proc/sys/kernel/random/boot_id") && len(words) == 2:
		return set(OpBootID)
	case !c.Sudo && is("ss", "-ltnpH") && len(words) == 2:
		return set(OpListeners)
	case c.Sudo && is("true") && len(words) == 1:
		return set(OpSudoCheck)
	case is("touch", ".hushlogin"):
		return set(OpHushlogin)
	case is("df", "-P", "/"):
		return set(OpDisk)
	case c.Sudo && is("systemctl", "reboot") && len(words) == 2:
		return set(OpReboot)
	case is("docker", "compose", "version"):
		return set(OpDockerCheck)
	case is("docker", "compose", "--project-directory") && len(words) >= 5:
		dir, rest := words[3], words[4:]
		switch rest[0] {
		case "pull":
			return set(OpComposePull, dir)
		case "up":
			return set(OpComposeUp, dir)
		case "down":
			return set(OpComposeDown, dir, boolArg(len(rest) > 1 && rest[1] == "-v"))
		case "ps":
			return set(OpComposePS, dir)
		case "restart":
			return set(OpComposeRestart, dir)
		case "images":
			return set(OpComposeImages, dir)
		case "logs":
			if len(rest) >= 5 && rest[1] == "--no-color" && rest[2] == "--tail" {
				return set(OpComposeLogs, dir, rest[4])
			}
		}
	case is("install", "-d") && indexOf(words, "--") > 0:
		return set(OpPrepareDirs, words[indexOf(words, "--")+1:]...)
	case is("rm", "-f", "--"):
		return set(OpRemove, words[3:]...)
	case is("curl", "-sk"):
		url := words[len(words)-1]
		resolve := ""
		if i := indexOf(words, "--resolve"); i > 0 && i+1 < len(words) {
			if parts := strings.Split(words[i+1], ":"); len(parts) == 3 {
				resolve = parts[2]
			}
		}
		return set(OpHTTP, url, resolve)
	case is("sh", "-c") && len(words) == 3 && strings.HasPrefix(words[2], "cd "):
		dir, command, ok := strings.Cut(strings.TrimPrefix(words[2], "cd "), " && ")
		if !ok {
			return Call{}, false
		}
		parts, err := Split(dir)
		if err != nil || len(parts) != 1 {
			return Call{}, false
		}
		return set(OpRun, parts[0], command)
	case is("sh", "-c") && len(words) >= 4:
		return set(Op(words[3]), words[4:]...)
	}
	return Call{}, false
}

func boolArg(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func indexOf(words []string, w string) int {
	for i, x := range words {
		if x == w {
			return i
		}
	}
	return -1
}
