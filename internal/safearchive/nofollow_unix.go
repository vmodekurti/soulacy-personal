//go:build unix

package safearchive

import "golang.org/x/sys/unix"

// noFollow makes the kernel refuse to open a symlink at the extraction target.
// Split by platform because Windows has no equivalent flag; there, O_EXCL
// carries the guarantee on its own.
const noFollow = unix.O_NOFOLLOW
