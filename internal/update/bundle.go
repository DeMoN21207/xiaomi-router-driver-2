package update

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type BundleInfo struct {
	Root        string `json:"-"`
	Binary      string `json:"binary"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	DefaultPort string `json:"defaultPort,omitempty"`
	PackageDir  string `json:"packageDir,omitempty"`
	DataDir     string `json:"dataDir,omitempty"`
	OpenVPNPath string `json:"openvpnPath,omitempty"`
	SingBoxPath string `json:"singboxPath,omitempty"`
	Version     string `json:"version,omitempty"`
	Commit      string `json:"commit,omitempty"`
	BuiltAt     string `json:"builtAt,omitempty"`
}

var ErrInvalidBundle = errors.New("invalid update bundle")

func ValidateBundle(root string) (BundleInfo, error) {
	bundleRoot, err := FindBundleRoot(root)
	if err != nil {
		return BundleInfo{}, err
	}

	info, err := ParseBundleInfo(filepath.Join(bundleRoot, "bundle-info.txt"))
	if err != nil {
		return BundleInfo{}, err
	}
	info.Root = bundleRoot

	if info.Binary != "vpn-manager" {
		return BundleInfo{}, fmt.Errorf("%w: binary must be vpn-manager", ErrInvalidBundle)
	}
	if info.GOOS != "linux" || info.GOARCH != "arm64" {
		return BundleInfo{}, fmt.Errorf("%w: unsupported target %s/%s", ErrInvalidBundle, info.GOOS, info.GOARCH)
	}

	required := []string{
		info.Binary,
		"start.sh",
		"bundle-info.txt",
		defaultPath(info.OpenVPNPath, "bin/openvpn"),
		defaultPath(info.SingBoxPath, "bin/sing-box"),
	}
	for _, name := range required {
		if err := requireNonEmptyRegularFile(bundleRoot, name); err != nil {
			return BundleInfo{}, err
		}
	}

	return info, nil
}

func FindBundleRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("%w: bundle root is empty", ErrInvalidBundle)
	}

	if _, err := os.Stat(filepath.Join(root, "bundle-info.txt")); err == nil {
		return root, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("check bundle-info.txt: %w", err)
	}

	var candidates []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "bundle-info.txt" {
			candidates = append(candidates, filepath.Dir(path))
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("scan bundle root: %w", err)
	}

	for _, candidate := range candidates {
		if hasBundleShape(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("%w: bundle-info.txt not found", ErrInvalidBundle)
}

func ParseBundleInfo(path string) (BundleInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return BundleInfo{}, fmt.Errorf("open bundle-info.txt: %w", err)
	}
	defer file.Close()

	var info BundleInfo
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "binary":
			info.Binary = value
		case "goos":
			info.GOOS = value
		case "goarch":
			info.GOARCH = value
		case "default_port":
			info.DefaultPort = value
		case "package_dir":
			info.PackageDir = value
		case "data_dir":
			info.DataDir = value
		case "openvpn_path":
			info.OpenVPNPath = value
		case "singbox_path":
			info.SingBoxPath = value
		case "version":
			info.Version = value
		case "commit":
			info.Commit = value
		case "built_at":
			info.BuiltAt = value
		}
	}
	if err := scanner.Err(); err != nil {
		return BundleInfo{}, fmt.Errorf("read bundle-info.txt: %w", err)
	}

	return info, nil
}

func hasBundleShape(root string) bool {
	_, err := os.Stat(filepath.Join(root, "vpn-manager"))
	return err == nil
}

func requireNonEmptyRegularFile(root string, name string) error {
	if filepath.IsAbs(name) || strings.Contains(filepath.Clean(name), "..") {
		return fmt.Errorf("%w: unsafe required path %s", ErrInvalidBundle, name)
	}
	path := filepath.Join(root, filepath.Clean(name))
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: required file missing: %s", ErrInvalidBundle, name)
		}
		return fmt.Errorf("check required file %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: required file is not regular: %s", ErrInvalidBundle, name)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%w: required file is empty: %s", ErrInvalidBundle, name)
	}
	return nil
}

func defaultPath(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
