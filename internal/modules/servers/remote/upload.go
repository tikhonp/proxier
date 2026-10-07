package remote

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// File is a rendered file to put on a server.
type File struct {
	Path    string // relative to the stack's directory, "/"-separated
	Mode    fs.FileMode
	Content []byte
}

// safeRel reports whether p is a plain relative path with no way out of its
// directory. Manifest validation guarantees it for templates; the check is
// here because this code runs as root-adjacent on someone else's machine.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

// UploadFiles is the upload-files step: it creates dir and the directories
// the files sit in (owned by the deploy user), uploads each file under a
// temporary name and renames it into place, then removes the files of the
// server's previous deployment (previous: paths relative to dir) that these
// no longer include. It touches nothing else in dir (runtime data such as
// certbot/). removed lists what it deleted.
func UploadFiles(ctx context.Context, env Env, c Conn, dir string, files []File, previous []string) (removed []string, err error) {
	if !strings.HasPrefix(dir, "/") || path.Clean(dir) != dir {
		return nil, fmt.Errorf("the stack directory %q is not a clean absolute path", dir)
	}
	dirs := map[string]bool{dir: true}
	keep := map[string]bool{}
	for _, f := range files {
		if !safeRel(f.Path) {
			return nil, fmt.Errorf("refusing to upload %q: not a plain relative path", f.Path)
		}
		keep[f.Path] = true
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			dirs[path.Join(dir, d)] = true
		}
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Strings(list) // parents sort before their children
	if _, err := env.run(ctx, c, "create directories", CmdPrepareDirs(list), RunOpts{}); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := c.Upload(ctx, path.Join(dir, f.Path), f.Content, f.Mode); err != nil {
			return nil, fmt.Errorf("upload %s: %w", f.Path, err)
		}
		env.info("Uploaded %s (%04o)", f.Path, f.Mode.Perm())
	}
	var gone []string
	for _, p := range previous {
		if keep[p] {
			continue
		}
		if !safeRel(p) {
			return nil, fmt.Errorf("refusing to remove %q: not a plain relative path", p)
		}
		gone = append(gone, path.Join(dir, p))
		removed = append(removed, p)
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		sort.Strings(removed)
		if _, err := env.run(ctx, c, "remove old files", CmdRemove(gone), RunOpts{}); err != nil {
			return nil, err
		}
		for _, p := range removed {
			env.info("Removed %s (not in this version)", p)
		}
	}
	return removed, nil
}
