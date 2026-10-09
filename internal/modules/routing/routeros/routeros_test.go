package routeros

import "testing"

func TestKeyCommands(t *testing.T) {
	got := KeyCommands("proxier", "ssh-ed25519 AAAA proxier@home\n")
	want := "/user group add name=proxier policy=read,write,ftp,ssh\n/user add name=proxier group=proxier\n/user ssh-keys add user=proxier key=\"ssh-ed25519 AAAA proxier@home\""
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
