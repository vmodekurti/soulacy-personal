//go:build unix

package config

import "syscall"

func syscallUmask(mask int) int { return syscall.Umask(mask) }
