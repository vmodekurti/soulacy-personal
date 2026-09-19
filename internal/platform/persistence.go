package platform

import (
	"fmt"
	"syscall"
)

// WorkspaceDurable reports whether the workspace directory will survive a
// redeploy, and if not, why.
//
// The failure mode this guards against: on a container or managed platform, if
// the workspace sits on the container's own (ephemeral) filesystem rather than a
// mounted volume, the next image redeploy silently destroys every agent, memory
// row, secret and setting. On a Host the workspace is always on real disk, so
// this only ever warns for Container/Managed deployments.
//
// The test is device identity: a mounted volume (or bind mount) is a different
// filesystem than the container root ("/"), so its device id differs. When the
// workspace shares the root device on a container/managed platform, it is
// ephemeral. When the device can't be read, it stays silent rather than crying
// wolf.
func WorkspaceDurable(workspace string) (durable bool, reason string) {
	return workspaceDurable(Detect(), workspace, deviceID)
}

func workspaceDurable(info Info, workspace string, dev func(string) (uint64, error)) (bool, string) {
	if info.Kind == Host {
		return true, ""
	}
	wsDev, err1 := dev(workspace)
	rootDev, err2 := dev("/")
	if err1 != nil || err2 != nil {
		return true, "" // can't determine — do not raise a false alarm
	}
	if wsDev == rootDev {
		return false, fmt.Sprintf(
			"workspace %q is on the container's ephemeral filesystem on %s — mount a persistent volume there or all data is lost on the next redeploy",
			workspace, info.Name)
	}
	return true, ""
}

func deviceID(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
}
