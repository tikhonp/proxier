package routers_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
)

func TestJumpUserCommands(t *testing.T) {
	got := routers.JumpUserCommands("proxier", "ssh-ed25519 AAAA proxier@home\n")
	want := "sudo useradd --create-home --shell /usr/sbin/nologin --password '*' proxier\n" +
		"sudo mkdir -p -m 700 ~proxier/.ssh\n" +
		"echo 'restrict,port-forwarding ssh-ed25519 AAAA proxier@home' | sudo tee -a ~proxier/.ssh/authorized_keys >/dev/null\n" +
		"sudo chmod 600 ~proxier/.ssh/authorized_keys\n" +
		"sudo chown -R proxier: ~proxier/.ssh"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
