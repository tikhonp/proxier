package templates_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

const gitManifest = "name: From git\nfiles:\n  - { path: compose.yaml }\n"

func (e *env) gitFetcher(max int64) templates.GitFetcher {
	return templates.GitFetcher{Client: http.DefaultClient, AllowHTTP: true, MaxTransfer: max, Timeout: 20 * time.Second}
}

func TestGitImportRecordsCommit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g := newGitHost(t)
	c1 := g.commit("first", map[string][]byte{
		"stacks/web/manifest.yaml": []byte(gitManifest),
		"stacks/web/compose.yaml":  []byte("services: {web: {image: 'nginx:1'}}\n"),
		"README.md":                []byte("not part of the template\n"),
	})
	c2 := g.commit("second", map[string][]byte{"stacks/web/compose.yaml": []byte("services: {web: {image: 'nginx:2'}}\n")})
	g.branch("main", c2)
	g.tag("v1", c1)
	f := e.gitFetcher(0)
	src := templates.GitSource{URL: g.URL(), Path: "stacks/web"}

	// a commit SHA: the draft records that SHA and holds that commit's files
	src.Ref = c1
	files, commit, err := f.Fetch(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if commit != c1 || string(files["compose.yaml"]) != "services: {web: {image: 'nginx:1'}}\n" || len(files) != 2 {
		t.Fatalf("at the SHA: %s %v", commit, files)
	}
	if g.posts.Load() != 1 {
		t.Errorf("a host that allows SHA wants should be asked once, was %d times", g.posts.Load())
	}
	id, err := e.Svc.Import(ctx, templates.ImportTarget{Name: "From git", Slug: "from-git"}, files,
		templates.Source{Kind: "git", URL: src.URL, Ref: src.Ref, Commit: commit, Path: src.Path}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.Svc.Draft(ctx, id)
	if d.Source.Kind != "git" || d.Source.Commit != c1 || d.Source.Path != "stacks/web" || d.Revision != 1 || d.BasedOn != 0 {
		t.Errorf("draft source %+v, revision %d", d.Source, d.Revision)
	}

	// a tag
	src.Ref = "v1"
	if _, commit, err = f.Fetch(ctx, src); err != nil || commit != c1 {
		t.Errorf("tag v1: %s %v", commit, err)
	}

	// a branch, later: a new draft (replacing the old) with the new commit
	src.Ref = "main"
	files, commit, err = f.Fetch(ctx, src)
	if err != nil || commit != c2 || string(files["compose.yaml"]) != "services: {web: {image: 'nginx:2'}}\n" {
		t.Fatalf("branch main: %s %v %v", commit, files, err)
	}
	if _, err := e.Svc.Import(ctx, templates.ImportTarget{TemplateID: id}, files,
		templates.Source{Kind: "git", URL: src.URL, Ref: src.Ref, Commit: commit, Path: src.Path}, "admin"); err != nil {
		t.Fatal(err)
	}
	d, _ = e.Svc.Draft(ctx, id)
	if d.Source.Commit != c2 || d.Source.Ref != "main" || d.Revision != 2 {
		t.Errorf("after re-import: %+v revision %d", d.Source, d.Revision)
	}

	// the default branch when no ref is given
	src.Ref = ""
	if _, commit, err = f.Fetch(ctx, src); err != nil || commit != c2 {
		t.Errorf("default branch: %s %v", commit, err)
	}
	// a ref that does not exist
	src.Ref = "nope"
	if _, _, err = f.Fetch(ctx, src); !errors.Is(err, templates.ErrGitRefNotFound) {
		t.Errorf("unknown ref: %v", err)
	}
}

func TestGitImportNeedsManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g := newGitHost(t)
	g.commit("only", map[string][]byte{"stacks/web/manifest.yaml": []byte(gitManifest), "docs/notes.md": []byte("hi\n"), "docs/more/x.md": []byte("hi\n")})
	f := e.gitFetcher(0)
	if _, _, err := f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: "master", Path: "docs"}); !errors.Is(err, templates.ErrManifestNotFound) {
		t.Errorf("a path without a manifest: %v", err)
	}
	if _, _, err := f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: "master", Path: "nothing/here"}); !errors.Is(err, templates.ErrGitPathNotFound) {
		t.Errorf("a path that does not exist: %v", err)
	}
	// the repository root has no manifest either; one folder up from it is the rule's "one top-level folder"
	if _, _, err := f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: "master"}); !errors.Is(err, templates.ErrManifestNotFound) {
		t.Errorf("the root: %v", err)
	}
	if _, _, err := f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: "master", Path: "../x"}); !errors.Is(err, templates.ErrUnsafePath) {
		t.Errorf("a path with ..: %v", err)
	}
	// only https without credentials in production
	for _, u := range []string{"http://example.com/r.git", "https://user:pw@example.com/r.git", "git@github.com:a/b.git", "file:///tmp/r"} {
		if _, _, err := (templates.GitFetcher{}).Fetch(ctx, templates.GitSource{URL: u}); !errors.Is(err, templates.ErrGitURL) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestGitImportCommitFallback(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g := newGitHost(t)
	g.refuseSHA = true
	c1 := g.commit("first", map[string][]byte{"manifest.yaml": []byte(gitManifest), "compose.yaml": []byte("services: {}\n")})
	c2 := g.commit("second", map[string][]byte{"compose.yaml": []byte("services: {web: {image: 'nginx:2'}}\n")})
	g.branch("main", c2)
	f := e.gitFetcher(0)

	// c1 is no ref tip: the host refuses the shallow want, the full clone finds it
	files, commit, err := f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: c1})
	if err != nil || commit != c1 || string(files["compose.yaml"]) != "services: {}\n" {
		t.Fatalf("fallback: %s %v %v", commit, files, err)
	}
	if g.refused.Load() != 1 || g.posts.Load() != 2 {
		t.Errorf("want one refused want then one full clone: refused %d, posts %d", g.refused.Load(), g.posts.Load())
	}
	// a host that does not even advertise SHA wants: the client never asks, the fallback still finds it
	g.hideSHA = true
	g.refused.Store(0)
	if _, commit, err = f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: c1}); err != nil || commit != c1 || g.refused.Load() != 0 {
		t.Errorf("host without SHA wants: %s %v (refused %d)", commit, err, g.refused.Load())
	}
	g.hideSHA = false

	// a commit the repository does not have
	if _, _, err := f.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: "0123456789abcdef0123456789abcdef01234567"}); !errors.Is(err, templates.ErrGitRefNotFound) {
		t.Errorf("an unknown commit: %v", err)
	}

	// over the transfer cap: refused, by the branch path and by both commit paths
	big := make([]byte, 256<<10)
	_, _ = rand.Read(big)
	c3 := g.commit("big", map[string][]byte{"blob.bin": big})
	g.branch("main", c3)
	small := e.gitFetcher(32 << 10)
	for name, ref := range map[string]string{"branch": "main", "commit": c3, "old commit": c1} {
		if _, _, err := small.Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: ref}); !errors.Is(err, templates.ErrGitTooLarge) {
			t.Errorf("%s over the cap: %v", name, err)
		}
	}
	// the same repository within a bigger cap works
	if _, commit, err := e.gitFetcher(8<<20).Fetch(ctx, templates.GitSource{URL: g.URL(), Ref: "main"}); err != nil || commit != c3 {
		t.Errorf("within the cap: %s %v", commit, err)
	}
}
