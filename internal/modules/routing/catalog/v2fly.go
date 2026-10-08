package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Limits of the v2fly archive.
const (
	archiveMax   = 32 << 20  // the tar.gz as downloaded
	unpackedMax  = 128 << 20 // every file of it, unpacked
	commitAnswer = 4 << 10
)

var (
	sha       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	v2flyName = regexp.MustCompile(`^[a-z0-9][a-z0-9._!-]{0,62}$`)
)

// errUnchanged: the source is as it was; nothing is written.
var errUnchanged = errors.New("catalog: unchanged")

// fetchV2fly asks GitHub for master's commit (one API request) and, when it
// isn't the one in force, downloads the archive at that commit (at master
// when the API failed) and builds its generation.
func (s *Service) fetchV2fly(ctx context.Context, cur store.CatalogSource) (generation, error) {
	ep := s.d.Endpoints
	hdr := http.Header{"Accept": {"application/vnd.github.sha"}}
	if tok, err := s.d.Settings.Get(ctx, conf.GitHubToken); err == nil && tok != "" {
		hdr.Set("Authorization", "Bearer "+tok)
	}
	commit := ""
	status, body, err := s.d.Fetch.GetWith(ctx, ep.GitHubAPI+"repos/v2fly/domain-list-community/commits/master", commitAnswer, hdr)
	if err == nil && status == http.StatusOK {
		if c := strings.TrimSpace(string(body)); sha.MatchString(c) {
			commit = c
		}
	}
	if ctx.Err() != nil {
		return generation{}, ctx.Err()
	}
	if commit != "" && commit == cur.Revision && cur.Generation > 0 {
		return generation{}, errUnchanged
	}
	ref := commit
	if ref == "" {
		ref = "master"
	}
	u := ep.Codeload + "v2fly/domain-list-community/tar.gz/" + ref
	status, body, err = s.d.Fetch.Get(ctx, u, archiveMax)
	if err != nil {
		return generation{}, err
	}
	if status != http.StatusOK {
		return generation{}, &sources.HTTPError{URL: u, Status: status}
	}
	files, err := unpack(body)
	if err != nil {
		return generation{}, err
	}
	g := build(files)
	g.revision = commit
	return g, nil
}

// unpack reads every file under <top>/data/ of a codeload archive.
func unpack(archive []byte) (map[string]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("v2fly archive: %w", err)
	}
	tr := tar.NewReader(gz)
	files := map[string]string{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("v2fly archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.Split(h.Name, "/")
		if len(parts) != 3 || parts[1] != "data" || !v2flyName.MatchString(parts[2]) {
			continue
		}
		if total += h.Size; h.Size > sources.MaxListBytes || total > unpackedMax {
			return nil, fmt.Errorf("%w: the v2fly archive", sources.ErrTooBig)
		}
		b, err := io.ReadAll(io.LimitReader(tr, sources.MaxListBytes))
		if err != nil {
			return nil, fmt.Errorf("v2fly archive: %w", err)
		}
		files[parts[2]] = string(b)
	}
	if len(files) == 0 {
		return nil, errors.New("v2fly archive: no lists under data/")
	}
	return files, nil
}

// build parses every list and resolves its includes in memory: one entry per
// list with its resolved count, a reverse-index row per direct entry, a row
// per include with its filter. A list whose include is missing counts what
// resolves without it.
func build(files map[string]string) generation {
	parsed := make(map[string]sources.List, len(files))
	for name, text := range files {
		parsed[name] = sources.ParseList(text)
	}
	local := &sources.Local{Files: parsed}
	g := generation{source: "v2fly"}
	for name, l := range parsed {
		count := 0
		if entries, err := local.Resolve(name); err == nil {
			count = sources.Count(entries)
		} else {
			count = sources.Count(l.Entries)
		}
		g.entries = append(g.entries, store.CatalogEntry{Source: "v2fly", Kind: "list", Name: name, Domains: count})
		for _, e := range l.Entries {
			attrs := make([]string, 0, len(e.Attrs))
			for _, a := range e.Attrs {
				attrs = append(attrs, "@"+a)
			}
			g.domains = append(g.domains, store.CatalogDomain{
				Source: "v2fly", Name: name, Domain: e.Name, Exact: e.Exact, Attrs: strings.Join(attrs, " "),
			})
		}
		for _, inc := range l.Includes {
			g.includes = append(g.includes, store.CatalogInclude{List: name, Included: inc.Name, Filter: inc.Filter()})
		}
	}
	return g
}
