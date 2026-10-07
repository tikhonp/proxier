package remote_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
)

func TestUploadRemovesOnlyOwnFiles(t *testing.T) {
	env := serverstest.NewSSHEnv(t)
	v := serverstest.NewVPS(t)
	c := env.Access(v)
	ctx := context.Background()

	first := []remote.File{
		{Path: "compose.yaml", Mode: 0o644, Content: []byte("services: {}\n")},
		{Path: "nginx/default.conf", Mode: 0o644, Content: []byte("server {}\n")},
		{Path: "old.txt", Mode: 0o644, Content: []byte("gone soon\n")},
		{Path: ".env", Mode: 0o600, Content: []byte("A=1\n")},
	}
	if removed, err := remote.UploadFiles(ctx, remote.Env{}, c, stackDir, first, nil); err != nil || len(removed) != 0 {
		t.Fatalf("first upload: %v %v", removed, err)
	}
	// Runtime data the stack made itself.
	v.PutFile(stackDir+"/certbot/conf/live/h/fullchain.pem", []byte("cert"))
	v.PutFile(stackDir+"/certbot/www/x", []byte("x"))

	second := []remote.File{
		{Path: "compose.yaml", Mode: 0o644, Content: []byte("services: {a: {}}\n")},
		{Path: "nginx/default.conf", Mode: 0o644, Content: []byte("server {listen 443;}\n")},
		{Path: ".env", Mode: 0o600, Content: []byte("A=2\n")},
	}
	removed, err := remote.UploadFiles(ctx, remote.Env{}, c, stackDir, second, []string{"compose.yaml", "nginx/default.conf", "old.txt", ".env"})
	if err != nil || !reflect.DeepEqual(removed, []string{"old.txt"}) {
		t.Fatalf("second upload: %v %v", removed, err)
	}
	got := v.Files(stackDir)
	want := []string{".env", "certbot/conf/live/h/fullchain.pem", "certbot/www/x", "compose.yaml", "nginx/default.conf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files on the server: %v, want %v", got, want)
	}
	if b, _ := v.File(stackDir + "/compose.yaml"); string(b) != "services: {a: {}}\n" {
		t.Errorf("compose.yaml = %q", b)
	}
	// No temporary file is left behind.
	for _, f := range got {
		if strings.HasSuffix(f, ".proxier-tmp") {
			t.Errorf("leftover %s", f)
		}
	}
}

func TestUploadRefusesUnsafePaths(t *testing.T) {
	env := serverstest.NewSSHEnv(t)
	v := serverstest.NewVPS(t)
	c := env.Access(v)
	ctx := context.Background()
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../b", "a//b", "./a", "a\\b", ""} {
		_, err := remote.UploadFiles(ctx, remote.Env{}, c, stackDir, []remote.File{{Path: p, Mode: 0o644, Content: []byte("x")}}, nil)
		if err == nil {
			t.Errorf("uploaded %q", p)
		}
	}
	// Nor will it remove outside the directory, whatever the database says.
	_, err := remote.UploadFiles(ctx, remote.Env{}, c, stackDir, nil, []string{"../../etc/passwd"})
	if err == nil {
		t.Error("removed a path outside the directory")
	}
	if _, err := remote.UploadFiles(ctx, remote.Env{}, c, "/opt/../etc", nil, nil); err == nil {
		t.Error("accepted an unclean directory")
	}
}
