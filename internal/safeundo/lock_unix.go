//go:build darwin || linux

package safeundo

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// One process owns recovery of this ledger. A second gateway cannot mark a
// live request interrupted. Keep the lock file after close (unlink races).
func lockLedger(path string) (*os.File, error) {
	fd, err := unix.Open(path+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path+".lock")
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("safe undo: unsafe ledger lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("safe undo: ledger is already in use")
	}
	return f, nil
}
