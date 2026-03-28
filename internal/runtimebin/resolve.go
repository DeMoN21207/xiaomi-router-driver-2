package runtimebin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Resolve returns an explicit binary path when one is configured or when a
// colocated executable exists next to one of the supplied search directories.
func Resolve(configured string, baseName string, searchDirs ...string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}

	for _, candidate := range candidates(baseName, searchDirs...) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}

	return baseName
}

func candidates(baseName string, searchDirs ...string) []string {
	names := []string{baseName}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(baseName), ".exe") {
		names = append([]string{baseName + ".exe"}, names...)
	}

	paths := make([]string, 0, len(names)*len(searchDirs)*3)
	seen := make(map[string]struct{}, len(paths))
	for _, dir := range searchDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		for _, name := range names {
			for _, candidate := range []string{
				filepath.Join(dir, name),
				filepath.Join(dir, "bin", name),
				filepath.Join(dir, ".vpn-manager", "bin", name),
			} {
				if _, exists := seen[candidate]; exists {
					continue
				}
				seen[candidate] = struct{}{}
				paths = append(paths, candidate)
			}
		}
	}

	return paths
}
