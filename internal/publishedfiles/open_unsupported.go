//go:build !linux && !darwin

package publishedfiles

import "os"

// Fail closed until this platform has a race-resistant, no-follow implementation.
func openPath(_, _ string, _ bool) (*os.File, error)      { return nil, ErrUnavailable }
func entryInfo(_ *os.File, _ string) (os.FileInfo, error) { return nil, ErrUnavailable }
func singleLink(_ os.FileInfo) bool                       { return false }
