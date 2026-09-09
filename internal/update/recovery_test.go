package update

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingArchiveReader struct{}

func (failingArchiveReader) Read([]byte) (int, error) {
	return 0, errors.New("upload interrupted")
}

type cancelArchiveReader struct{ cancel context.CancelFunc }

func (r cancelArchiveReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, io.EOF
}

func TestUploadedUpdateRemovesWorkFiles(t *testing.T) {
	for _, scenario := range []string{"interrupted", "invalid", "success", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			appDir, dataDir := t.TempDir(), t.TempDir()
			writeRuntimeFile(t, appDir, "vpn-manager", "old manager")
			manager := NewManager(Options{AppDir: appDir, DataDir: dataDir, RuntimeOS: "linux"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var reader io.Reader = strings.NewReader("not gzip")
			if scenario == "interrupted" {
				reader = io.MultiReader(strings.NewReader("partial archive"), failingArchiveReader{})
			}
			if scenario == "success" || scenario == "canceled" {
				archive := filepath.Join(t.TempDir(), "bundle.tar.gz")
				createTarGz(t, archive, map[string]string{
					"vpn-manager": "new manager", "start.sh": "#!/bin/sh\n",
					"bin/openvpn": "openvpn", "bin/sing-box": "sing-box",
					"bundle-info.txt": "binary=vpn-manager\ngoos=linux\ngoarch=arm64\n",
				})
				file, err := os.Open(archive)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				reader = file
				if scenario == "canceled" {
					reader = io.MultiReader(file, cancelArchiveReader{cancel: cancel})
				}
			}
			result, err := manager.InstallUploaded(ctx, reader, "bundle.tar.gz")
			if (err == nil) != (scenario == "success") {
				t.Fatalf("install error = %v", err)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("install error = %v, want canceled", err)
			}
			if scenario != "success" && readFile(t, filepath.Join(appDir, "vpn-manager")) != "old manager" {
				t.Fatal("failed/canceled upload replaced the runtime")
			}
			if scenario == "success" && readFile(t, filepath.Join(result.BackupDir, "vpn-manager")) != "old manager" {
				t.Fatal("cleanup lost rollback backup")
			}
			entries, err := os.ReadDir(filepath.Join(dataDir, ".vpn-manager", "updates"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("update left %d work directories with archive/extracted files", len(entries))
			}
		})
	}
}
