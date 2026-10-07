package templates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Git import refusals; pages translate them.
var (
	ErrGitURL          = errors.New("use an https:// repository URL without credentials")
	ErrGitRefNotFound  = errors.New("the ref was not found in the repository")
	ErrGitPathNotFound = errors.New("the path was not found in the repository")
	ErrGitTooLarge     = errors.New("the repository is over the 100 MiB transfer limit")
)

// GitSource is where to import from: a public HTTPS repository, a ref (a
// branch, a tag or a full 40-hex commit; empty is the default branch) and a
// folder inside it ("" is the root).
type GitSource struct{ URL, Ref, Path string }

// GitFetcher fetches a path of a repository into memory. The zero value is
// what production uses; tests set Client to reach an httptest server and
// AllowHTTP because it is not https.
type GitFetcher struct {
	Client      *http.Client  // nil: http.DefaultTransport
	AllowHTTP   bool          // tests only
	MaxTransfer int64         // bytes received; 0 = 100 MiB
	Timeout     time.Duration // 0 = 60 s
}

const (
	defaultGitTransfer = 100 << 20
	defaultGitTimeout  = 60 * time.Second
	importRef          = plumbing.ReferenceName("refs/proxier/import")
)

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// FetchGit imports with the production settings.
func FetchGit(ctx context.Context, src GitSource) (files map[string][]byte, commit string, err error) {
	return GitFetcher{}.Fetch(ctx, src)
}

// Fetch clones the repository in memory and returns the files under src.Path
// (the manifest rule applied to them) with the commit they come from. A
// branch or tag is fetched shallow. A commit is first fetched shallow by its
// SHA; many hosts refuse a want for an unadvertised commit, so then every
// branch is cloned in full and the commit looked up in it. Both are bounded by
// the timeout and the transfer cap.
func (g GitFetcher) Fetch(ctx context.Context, src GitSource) (map[string][]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(src.URL))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && (!g.AllowHTTP || u.Scheme != "http")) {
		return nil, "", ErrGitURL
	}
	sub, err := cleanRepoPath(src.Path)
	if err != nil {
		return nil, "", err
	}
	installGitTransport()
	timeout := g.Timeout
	if timeout == 0 {
		timeout = defaultGitTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st := &fetchState{rt: http.DefaultTransport, max: g.MaxTransfer, allowHTTP: g.AllowHTTP}
	if g.Client != nil && g.Client.Transport != nil {
		st.rt = g.Client.Transport
	}
	if st.max == 0 {
		st.max = defaultGitTransfer
	}
	ctx = context.WithValue(ctx, stateKey{}, st)

	commit, err := g.fetchCommit(ctx, u.String(), strings.TrimSpace(src.Ref), st)
	if err != nil {
		if st.exceeded.Load() {
			return nil, "", ErrGitTooLarge
		}
		return nil, "", err
	}
	files, err := treeFiles(commit, sub)
	if err != nil {
		return nil, "", err
	}
	files, err = StripRoot(files)
	if err != nil {
		return nil, "", err
	}
	return files, commit.Hash.String(), nil
}

func cleanRepoPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return "", nil
	}
	return cleanName(p)
}

func (g GitFetcher) fetchCommit(ctx context.Context, repoURL, ref string, st *fetchState) (*object.Commit, error) {
	if commitSHA.MatchString(ref) {
		if c, err := fetchSHA(ctx, repoURL, ref); err == nil {
			return c, nil
		} else if st.exceeded.Load() || ctx.Err() != nil {
			return nil, err
		}
		return cloneAll(ctx, repoURL, ref)
	}
	name, err := resolveRef(ctx, repoURL, ref)
	if err != nil {
		return nil, err
	}
	repo, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
		URL: repoURL, ReferenceName: name, SingleBranch: true, Depth: 1, Tags: git.NoTags,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", name.Short(), err)
	}
	head, err := repo.Reference(name, true)
	if err != nil {
		return nil, ErrGitRefNotFound
	}
	return repo.CommitObject(head.Hash())
}

// resolveRef finds the full name of a branch or tag by listing the remote's
// refs (nothing is downloaded). An empty ref is HEAD.
func resolveRef(ctx context.Context, repoURL, ref string) (plumbing.ReferenceName, error) {
	if ref == "" {
		return plumbing.HEAD, nil
	}
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{repoURL}})
	refs, err := rem.ListContext(ctx, &git.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listing the repository: %w", err)
	}
	want := []plumbing.ReferenceName{plumbing.ReferenceName(ref), plumbing.NewBranchReferenceName(ref), plumbing.NewTagReferenceName(ref)}
	for _, w := range want {
		for _, r := range refs {
			if r.Name() == w && strings.HasPrefix(string(w), "refs/") {
				return w, nil
			}
		}
	}
	return "", ErrGitRefNotFound
}

// fetchSHA asks for exactly one commit, shallow.
func fetchSHA(ctx context.Context, repoURL, sha string) (*object.Commit, error) {
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		return nil, err
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{repoURL}}); err != nil {
		return nil, err
	}
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin", Depth: 1, Tags: git.NoTags,
		RefSpecs: []config.RefSpec{config.RefSpec("+" + sha + ":" + string(importRef))},
	})
	if err != nil {
		return nil, err
	}
	r, err := repo.Reference(importRef, true)
	if err != nil {
		return nil, err
	}
	return repo.CommitObject(r.Hash())
}

// cloneAll is the fallback for a commit: every branch, full history, in
// memory; the commit must be reachable from one of them.
func cloneAll(ctx context.Context, repoURL, sha string) (*object.Commit, error) {
	repo, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{URL: repoURL, Tags: git.NoTags})
	if err != nil {
		return nil, fmt.Errorf("cloning the repository: %w", err)
	}
	c, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, ErrGitRefNotFound
	}
	return c, nil
}

// treeFiles reads the files under dir ("" for the root) of a commit's tree,
// refusing symlinks and over-limit trees.
func treeFiles(c *object.Commit, dir string) (map[string][]byte, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	if dir != "" {
		if tree, err = tree.Tree(dir); err != nil {
			return nil, ErrGitPathNotFound
		}
	}
	files := map[string][]byte{}
	left := int64(MaxImportBytes)
	it := tree.Files()
	defer it.Close()
	for {
		f, err := it.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if f.Mode == filemode.Symlink {
			return nil, ErrUnsafePath
		}
		if skipped(f.Name) {
			continue
		}
		if len(files) >= MaxImportFiles {
			return nil, ErrTooManyFiles
		}
		if left -= f.Size; left < 0 {
			return nil, ErrTooLarge
		}
		name, err := cleanName(f.Name)
		if err != nil {
			return nil, err
		}
		body, err := f.Contents()
		if err != nil {
			return nil, err
		}
		files[name] = []byte(body)
	}
}

// ---- transport ------------------------------------------------------------

// go-git picks its HTTP client from a process-wide registry, so one transport
// is installed once. It is told what to do per fetch by a value in the request
// context: the round tripper to use, the byte cap and whether http is allowed.
type stateKey struct{}

type fetchState struct {
	rt        http.RoundTripper
	max       int64
	allowHTTP bool
	read      atomic.Int64
	exceeded  atomic.Bool
}

var installOnce sync.Once

func installGitTransport() {
	installOnce.Do(func() {
		hc := &http.Client{Transport: stateTransport{}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			st, _ := req.Context().Value(stateKey{}).(*fetchState)
			if req.URL.Scheme != "https" && (st == nil || !st.allowHTTP) {
				return ErrGitURL
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		}}
		c := githttp.NewClient(hc)
		client.InstallProtocol("https", c)
		client.InstallProtocol("http", c)
	})
}

type stateTransport struct{}

func (stateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	st, _ := req.Context().Value(stateKey{}).(*fetchState)
	rt := http.DefaultTransport
	if st != nil {
		rt = st.rt
	}
	resp, err := rt.RoundTrip(req)
	if err != nil || st == nil {
		return resp, err
	}
	resp.Body = &capBody{rc: resp.Body, st: st}
	return resp, nil
}

// capBody counts what a fetch receives and fails the read past the cap.
type capBody struct {
	rc io.ReadCloser
	st *fetchState
}

func (b *capBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if b.st.read.Add(int64(n)) > b.st.max {
		b.st.exceeded.Store(true)
		return n, ErrGitTooLarge
	}
	return n, err
}

func (b *capBody) Close() error { return b.rc.Close() }
