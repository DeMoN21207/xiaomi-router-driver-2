//go:build !linux

package status

// readDiskUsage is a no-op on non-Linux hosts used for local tooling.
func readDiskUsage(totalMB, usedMB *uint64, freePct *float64, path string) {
}
