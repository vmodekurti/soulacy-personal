//go:build linux || darwin

package publishedfiles

import (
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Every untrusted path component is opened relative to a pinned directory
// descriptor with O_NOFOLLOW. Unlike check-then-open, renaming or replacing a
// component with a symlink cannot win a traversal race. The configured absolute
// root and its ancestors must remain operator-controlled, not agent-writable.
func openPath(root, relative string, directory bool) (*os.File, error) {
	if !ValidRoot(root) {
		return nil, ErrUnavailable
	}
	if !ValidPath(relative) {
		return nil, ErrPath
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Open(root, flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	if relative != "" {
		parts := strings.Split(relative, "/")
		for i, part := range parts {
			componentFlags := flags
			if i < len(parts)-1 || directory {
				componentFlags |= unix.O_DIRECTORY
			}
			next, openErr := unix.Openat(fd, part, componentFlags, 0)
			unix.Close(fd)
			if openErr != nil {
				return nil, ErrNotFound
			}
			fd = next
		}
	}
	return os.NewFile(uintptr(fd), relative), nil
}

func entryInfo(dir *os.File, name string) (os.FileInfo, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	return directoryEntryInfo{name: name, stat: stat}, nil
}

type directoryEntryInfo struct {
	name string
	stat unix.Stat_t
}

func (i directoryEntryInfo) Name() string { return i.name }
func (i directoryEntryInfo) Size() int64  { return i.stat.Size }
func (i directoryEntryInfo) Mode() os.FileMode {
	switch i.stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return os.ModeDir
	case unix.S_IFREG:
		return 0
	default:
		return os.ModeIrregular
	}
}
func (i directoryEntryInfo) ModTime() time.Time {
	return time.Unix(int64(i.stat.Mtim.Sec), int64(i.stat.Mtim.Nsec))
}
func (i directoryEntryInfo) IsDir() bool { return i.Mode().IsDir() }
func (i directoryEntryInfo) Sys() any    { return &i.stat }

func singleLink(info os.FileInfo) bool {
	if info.IsDir() {
		return true
	}
	if stat, ok := info.Sys().(*unix.Stat_t); ok {
		return stat.Nlink == 1
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
