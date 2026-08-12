//go:build !unix

package config

func syscallUmask(mask int) int { return 0 }
