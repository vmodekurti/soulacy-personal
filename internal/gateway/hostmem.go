package gateway

import "github.com/shirou/gopsutil/v4/mem"

// hostMemoryGB returns total system memory in whole gigabytes, or 0 when it
// cannot be determined. Zero means "do not gate", which is the safe direction:
// wrongly hiding a model the machine can run is worse than offering one it
// cannot, because the second failure is visible and recoverable.
func hostMemoryGB() int {
	v, err := mem.VirtualMemory()
	if err != nil || v == nil || v.Total == 0 {
		return 0
	}
	return int(v.Total / (1024 * 1024 * 1024))
}
