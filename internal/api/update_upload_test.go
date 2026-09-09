package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"xiomi-router-driver/internal/update"
)

func TestUpdateUploadStreamsValidBundleAndPreservesData(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"vpn-manager": "new manager", "start.sh": "#!/bin/sh\n",
		"bin/openvpn": "openvpn", "bin/sing-box": "sing-box",
		"bundle-info.txt": "binary=vpn-manager\ngoos=linux\ngoarch=arm64\n",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("note", "uploaded from the panel"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("archive", "router.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"vpn-manager": "old manager", "data/state.db": "keep data"} {
		if err := os.WriteFile(filepath.Join(appDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(Dependencies{Update: update.NewManager(update.Options{RuntimeOS: "linux", AppDir: appDir, DataDir: dataDir})})
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	var result update.InstallResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		filepath.Join(appDir, "vpn-manager"):           "new manager",
		filepath.Join(dataDir, "state.db"):             "keep data",
		filepath.Join(result.BackupDir, "vpn-manager"): "old manager",
	} {
		got, err := os.ReadFile(name)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q / %v, want %q", name, got, err, want)
		}
	}
}

type uploadCountingReader struct {
	io.Reader
	read int
}

type signaledUploadReader struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *signaledUploadReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}

func (r *uploadCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestUpdateUploadRejectsUnavailableInstallerBeforeReadingArchive(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "busy"}[busy], func(t *testing.T) {
			manager := update.NewManager(update.Options{RuntimeOS: "darwin"})
			wantStatus := http.StatusServiceUnavailable
			if busy {
				manager = update.NewManager(update.Options{RuntimeOS: "linux", AppDir: t.TempDir(), DataDir: t.TempDir()})
				reader, writer := io.Pipe()
				started := make(chan struct{})
				done := make(chan struct{})
				go func() {
					_, _ = manager.InstallUploaded(context.Background(), &signaledUploadReader{Reader: reader, started: started}, "first.tar.gz")
					close(done)
				}()
				t.Cleanup(func() {
					_ = writer.Close()
					_ = reader.Close()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("first upload did not finish")
					}
				})
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("first upload did not start")
				}
				wantStatus = http.StatusConflict
			}
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("archive", "router.tar.gz")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(bytes.Repeat([]byte("x"), 4<<20)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			reader := &uploadCountingReader{Reader: bytes.NewReader(body.Bytes())}
			req := httptest.NewRequest(http.MethodPost, "/api/system/update/upload", reader)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			h := NewHandler(Dependencies{Update: manager})
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != wantStatus {
				t.Fatalf("status = %d: %s", response.Code, response.Body)
			}
			if reader.read > 64<<10 {
				t.Fatalf("rejected upload consumed %d bytes before checking installer availability", reader.read)
			}
		})
	}
}

func TestTruncatedUpdateUploadLeavesRuntimeAndDiskClean(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", "router.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "partial gzip upload without final boundary"); err != nil {
		t.Fatal(err)
	}
	appDir, dataDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "vpn-manager"), []byte("old manager"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Dependencies{Update: update.NewManager(update.Options{RuntimeOS: "linux", AppDir: appDir, DataDir: dataDir})})
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code < 400 {
		t.Fatalf("truncated upload returned success: %d", response.Code)
	}
	got, err := os.ReadFile(filepath.Join(appDir, "vpn-manager"))
	if err != nil || string(got) != "old manager" {
		t.Fatalf("runtime changed: %q / %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, ".vpn-manager", "updates"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary upload files left behind: %v / %v", entries, err)
	}
}

func BenchmarkUpdateUpload(b *testing.B) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", "router.tar.gz")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 16<<20)); err != nil {
		b.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		b.Fatal(err)
	}
	h := NewHandler(Dependencies{Update: update.NewManager(update.Options{
		RuntimeOS: "linux", AppDir: b.TempDir(), DataDir: b.TempDir(),
	})})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/system/update/upload", bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != http.StatusInternalServerError {
			b.Fatalf("status = %d", response.Code)
		}
	}
}
