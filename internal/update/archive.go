package update

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ExtractTarGz(archivePath string, targetDir string) error {
	input, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer input.Close()

	gz, err := gzip.NewReader(input)
	if err != nil {
		return fmt.Errorf("read gzip archive: %w", err)
	}
	defer gz.Close()

	targetAbs, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve extraction directory: %w", err)
	}
	if err := os.MkdirAll(targetAbs, 0o755); err != nil {
		return fmt.Errorf("prepare extraction directory: %w", err)
	}

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		entryPath, err := safeArchiveEntryPath(targetAbs, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(entryPath, modeOrDefault(header.FileInfo().Mode(), 0o755)); err != nil {
				return fmt.Errorf("create directory %s: %w", header.Name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
				return fmt.Errorf("prepare parent for %s: %w", header.Name, err)
			}
			output, err := os.OpenFile(entryPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, modeOrDefault(header.FileInfo().Mode(), 0o644))
			if err != nil {
				return fmt.Errorf("create file %s: %w", header.Name, err)
			}
			if _, copyErr := io.Copy(output, tr); copyErr != nil {
				_ = output.Close()
				return fmt.Errorf("extract file %s: %w", header.Name, copyErr)
			}
			if err := output.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", header.Name, err)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive links are not supported: %s", header.Name)
		default:
			return fmt.Errorf("unsupported archive entry type %c: %s", header.Typeflag, header.Name)
		}
	}

	return nil
}

func safeArchiveEntryPath(targetAbs string, name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "./")
	clean := filepath.Clean(name)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("empty archive entry path")
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive entry path: %s", name)
	}

	entryAbs, err := filepath.Abs(filepath.Join(targetAbs, clean))
	if err != nil {
		return "", fmt.Errorf("resolve archive entry path %s: %w", name, err)
	}
	rel, err := filepath.Rel(targetAbs, entryAbs)
	if err != nil {
		return "", fmt.Errorf("check archive entry path %s: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive entry path: %s", name)
	}

	return entryAbs, nil
}

func modeOrDefault(mode os.FileMode, fallback os.FileMode) os.FileMode {
	mode = mode.Perm()
	if mode == 0 {
		return fallback
	}
	return mode
}
