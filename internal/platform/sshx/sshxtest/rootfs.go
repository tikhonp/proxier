package sshxtest

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/pkg/sftp"
)

// rootFS serves a directory as if it were the whole file system: an absolute
// path like /opt/proxier/x lands in <root>/opt/proxier/x, so a test can upload
// to the paths a real server would have. Like OpenSSH, a plain rename refuses
// to replace and posix-rename replaces.
type rootFS struct {
	root   string
	refuse func() bool // writes fail while it says so
}

func (r *rootFS) handlers() sftp.Handlers {
	return sftp.Handlers{FileGet: r, FilePut: r, FileCmd: r, FileList: r}
}

// real maps a path of the fake file system to the host's.
func (r *rootFS) real(p string) string {
	// A path already inside the root is the host's own (what Server.Dir()
	// returned to the test), not one of the fake file system.
	if p == r.root || strings.HasPrefix(p, r.root+"/") {
		return filepath.Clean(p)
	}
	return filepath.Join(r.root, filepath.FromSlash(path.Clean("/"+p)))
}

func (r *rootFS) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	f, err := os.Open(r.real(req.Filepath))
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (r *rootFS) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	if r.refuse != nil && r.refuse() {
		return nil, os.ErrPermission
	}
	fl := req.Pflags()
	flags := os.O_WRONLY
	if fl.Creat {
		flags |= os.O_CREATE
	}
	if fl.Trunc {
		flags |= os.O_TRUNC
	}
	if fl.Excl {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(r.real(req.Filepath), flags, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (r *rootFS) Filecmd(req *sftp.Request) error {
	p := r.real(req.Filepath)
	switch req.Method {
	case "Setstat":
		if req.AttrFlags().Permissions {
			return os.Chmod(p, req.Attributes().FileMode().Perm())
		}
		return nil
	case "Rename":
		target := r.real(req.Target)
		if _, err := os.Lstat(target); err == nil {
			return syscall.EEXIST
		}
		return os.Rename(p, target)
	case "PosixRename":
		return os.Rename(p, r.real(req.Target))
	case "Remove":
		if st, err := os.Lstat(p); err == nil && st.IsDir() {
			return syscall.EISDIR
		}
		return os.Remove(p)
	case "Rmdir":
		return os.Remove(p)
	case "Mkdir":
		return os.Mkdir(p, 0o755)
	}
	return sftp.ErrSSHFxOpUnsupported
}

type lister []fs.FileInfo

func (l lister) ListAt(out []fs.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[offset:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}

func (r *rootFS) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	p := r.real(req.Filepath)
	switch req.Method {
	case "List":
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		out := make(lister, 0, len(entries))
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				out = append(out, info)
			}
		}
		return out, nil
	case "Stat", "Lstat":
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		return lister{info}, nil
	}
	return nil, errors.New("sshxtest: unsupported listing " + req.Method)
}
