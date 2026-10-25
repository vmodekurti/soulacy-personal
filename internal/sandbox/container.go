package sandbox

import (
	"os"
	"strconv"
	"strings"
)

// ContainerProfile is the hardened `docker run` boundary shared by every
// container execution path (privileged builtins, the docker executor). It is
// defined once so a hardening change reaches all of them; see
// docs/CONTAINER_SANDBOX_DESIGN.md.
//
// Unlike Limits/Wrap (rlimits, NOT a security boundary), a container built
// from this profile is one: read-only root, no capabilities, no privilege
// escalation, no network unless the operator chose one, and nothing from the
// host except the explicit Mounts.
type ContainerProfile struct {
	// Network is the docker network; "" means "none".
	Network string
	// Limits supplies memory, CPU, open-file and file-size caps. Zero knobs
	// emit no flag, matching the rlimit wrapper's semantics.
	Limits Limits
	// PIDs caps processes inside the container; 0 means 128.
	PIDs int
	// TmpfsMB sizes the writable, noexec /tmp; 0 means 64.
	TmpfsMB int
	// User is passed to --user; "" keeps the image's default user.
	User string
	// Workdir is the container working directory; "" keeps the image's.
	Workdir string
	// Mounts are -v specs (host:container[:ro]). Nothing is mounted
	// implicitly.
	Mounts []string
	// Env holds KEY=VALUE pairs the caller has ALREADY filtered. The profile
	// never forwards the host environment on its own.
	Env []string
}

// RunArgs returns the `docker run` arguments (without the binary) that start
// image with argv inside the profile's boundary.
func (p ContainerProfile) RunArgs(image string, argv ...string) []string {
	network := strings.TrimSpace(p.Network)
	if network == "" {
		network = "none"
	}
	args := []string{"run", "--rm", "-i", "--network", network, "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(positiveOr(p.PIDs, 128)),
	}
	if user := strings.TrimSpace(p.User); user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, "--tmpfs", "/tmp:rw,noexec,nosuid,size="+strconv.Itoa(positiveOr(p.TmpfsMB, 64))+"m")
	for _, m := range p.Mounts {
		if m = strings.TrimSpace(m); m != "" {
			args = append(args, "-v", m)
		}
	}
	if p.Workdir != "" {
		args = append(args, "-w", p.Workdir)
	}
	if p.Limits.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(p.Limits.MemoryMB)+"m")
	}
	if p.Limits.CPUSeconds > 0 {
		args = append(args, "--cpus", "1")
	}
	if p.Limits.OpenFiles > 0 {
		n := strconv.Itoa(p.Limits.OpenFiles)
		args = append(args, "--ulimit", "nofile="+n+":"+n)
	}
	if p.Limits.FileSizeMB > 0 {
		n := strconv.FormatInt(int64(p.Limits.FileSizeMB)<<20, 10)
		args = append(args, "--ulimit", "fsize="+n+":"+n)
	}
	for _, kv := range p.Env {
		if strings.IndexByte(kv, '=') > 0 {
			args = append(args, "-e", kv)
		}
	}
	args = append(args, image)
	return append(args, argv...)
}

// ContainerUser resolves an operator-facing user setting into a --user value:
//
//	"auto" (or "")  → the gateway's own uid:gid, so files written to mounts
//	                  are owned by the operator and the process is not root;
//	                  "" where the platform has no POSIX ids (Windows)
//	"image"         → "" (keep the image's default user)
//	anything else   → passed through verbatim (e.g. "1000:1000")
func ContainerUser(setting string) string {
	switch s := strings.TrimSpace(setting); strings.ToLower(s) {
	case "", "auto":
		uid, gid := os.Getuid(), os.Getgid()
		if uid < 0 || gid < 0 {
			return ""
		}
		return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
	case "image":
		return ""
	default:
		return s
	}
}

func positiveOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
