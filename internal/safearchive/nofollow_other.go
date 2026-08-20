//go:build !unix

package safearchive

// noFollow is a no-op on platforms without O_NOFOLLOW. O_EXCL still refuses to
// open an existing entry of any kind at the target, which is the guarantee
// that matters; what is given up is only the narrow race between the parent
// resolution check and the open.
const noFollow = 0
