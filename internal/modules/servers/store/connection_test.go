package store_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

func TestConnectIP(t *testing.T) {
	s := store.Server{IP: "203.0.113.10"}
	if s.ConnectIP() != s.IP {
		t.Fatal("legacy server must use its service IP")
	}
	s.IP = "203.0.113.11"
	if s.ConnectIP() != s.IP {
		t.Fatal("default connection must follow public IP changes")
	}
	s.SSHIP = "100.64.0.10"
	s.IP = "203.0.113.12"
	if s.ConnectIP() != "100.64.0.10" {
		t.Fatal("explicit management IP must survive public IP changes")
	}
}
