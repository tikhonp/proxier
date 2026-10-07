package templates_test

import (
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"
)

// gitHost is a smart-HTTP git server on go-git's own server package, with a
// repository the test commits to. refuseSHA makes it answer like a host that
// does not allow a want for a commit that is no advertised ref tip.
type gitHost struct {
	t         *testing.T
	repo      *git.Repository
	wt        *git.Worktree
	srv       *httptest.Server
	refuseSHA bool // advertises SHA wants but answers 403 to one for a commit that is no ref tip
	hideSHA   bool // does not advertise allow-reachable-sha1-in-want at all
	refused   atomic.Int32
	posts     atomic.Int32
}

type oneRepo struct{ s storer.Storer }

func (o oneRepo) Load(*transport.Endpoint) (storer.Storer, error) { return o.s, nil }

func newGitHost(t *testing.T) *gitHost {
	t.Helper()
	st := memory.NewStorage()
	repo, err := git.Init(st, memfs.New())
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	g := &gitHost{t: t, repo: repo, wt: wt}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

// URL is the repository's URL (http: the fetcher is told to allow it).
func (g *gitHost) URL() string { return g.srv.URL + "/repo.git" }

// commit writes files (a nil value deletes) and returns the new commit's SHA.
func (g *gitHost) commit(msg string, files map[string][]byte) string {
	g.t.Helper()
	for p, c := range files {
		if c == nil {
			_, _ = g.wt.Remove(p)
			continue
		}
		f, err := g.wt.Filesystem.Create(p)
		if err != nil {
			g.t.Fatal(err)
		}
		_, _ = f.Write(c)
		_ = f.Close()
		if _, err := g.wt.Add(p); err != nil {
			g.t.Fatal(err)
		}
	}
	h, err := g.wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		g.t.Fatal(err)
	}
	return h.String()
}

func (g *gitHost) tag(name, sha string) {
	g.t.Helper()
	if err := g.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName(name), plumbing.NewHash(sha))); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gitHost) branch(name, sha string) {
	g.t.Helper()
	if err := g.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), plumbing.NewHash(sha))); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gitHost) serve(w http.ResponseWriter, r *http.Request) {
	ep, _ := transport.NewEndpoint(g.URL())
	sess, err := server.NewServer(oneRepo{g.repo.Storer}).NewUploadPackSession(ep, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs"):
		ar, err := sess.AdvertisedReferencesContext(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if !g.hideSHA {
			_ = ar.Capabilities.Set(capability.AllowReachableSHA1InWant)
		}
		ar.Prefix = [][]byte{[]byte("# service=git-upload-pack"), pktline.Flush}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_ = ar.Encode(w)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-upload-pack"):
		g.posts.Add(1)
		body := r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			body = zr
		}
		req := packp.NewUploadPackRequest()
		if err := req.Decode(body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// the clients here start from nothing, so there are no haves to read.
		// go-git's server can't do shallow: it answers a depth request with the
		// full history, which a client takes as a deeper clone than asked for.
		shallow := req.Depth != nil && !req.Depth.IsZero()
		req.Depth = packp.DepthCommits(0)
		req.Capabilities.Delete(capability.Shallow)
		if g.refuseSHA && !g.allTips(req.Wants) {
			g.refused.Add(1)
			http.Error(w, "upload-pack: not our ref", http.StatusForbidden)
			return
		}
		resp, err := sess.UploadPack(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		if shallow {
			_, _ = w.Write([]byte("0000")) // an empty shallow-update section
		}
		_ = resp.Encode(w)
	default:
		http.NotFound(w, r)
	}
}

func (g *gitHost) allTips(wants []plumbing.Hash) bool {
	refs, _ := g.repo.References()
	tips := map[plumbing.Hash]bool{}
	_ = refs.ForEach(func(r *plumbing.Reference) error {
		tips[r.Hash()] = true
		return nil
	})
	for _, w := range wants {
		if !tips[w] {
			return false
		}
	}
	return true
}
