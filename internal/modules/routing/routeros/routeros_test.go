package routeros

import (
	"testing"
	"time"
)

func TestKeyCommands(t *testing.T) {
	got := KeyCommands("proxier", "ssh-ed25519 AAAA proxier@home\n")
	want := "/user group add name=proxier policy=read,write,ftp,ssh\n/user add name=proxier group=proxier password=[:rndstr length=32 from=\"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\"]\n/user ssh-keys add user=proxier key=\"ssh-ed25519 AAAA proxier@home\""
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestUploadTimeoutGrowsWithTheFile(t *testing.T) {
	if got := UploadTimeout(26); got != time.Minute {
		t.Fatalf("a one-line file: %s", got)
	}
	// A full file of MaxDomains names (about 530 KiB) still gets minutes.
	if got := UploadTimeout(530 << 10); got != time.Minute+530*time.Second {
		t.Fatalf("a full file: %s", got)
	}
}
