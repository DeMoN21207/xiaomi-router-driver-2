//go:build linux

package status

import "syscall"

// readDiskUsage uses syscall.Statfs to get disk space.
func readDiskUsage(totalMB, usedMB *uint64, freePct *float64, path string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bfree * uint64(stat.Bsize)
	usedBytes := totalBytes - freeBytes

	*totalMB = totalBytes / (1024 * 1024)
	*usedMB = usedBytes / (1024 * 1024)
	if totalBytes > 0 {
		*freePct = float64(freeBytes) / float64(totalBytes) * 100
	}
}
