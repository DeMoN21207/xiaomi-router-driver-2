package domains

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestManagerReplaceAllPersistsToSQLiteAndRuntimeFile(t *testing.T) {
	db := openDomainsTestDB(t)
	runtimePath := filepath.Join(t.TempDir(), ".vpn-manager", "domains.list")
	manager := NewManager(db, runtimePath, filepath.Join(t.TempDir(), "domains.list"))

	if err := manager.ReplaceAll([]string{"OpenAI.com", "chatgpt.com", "openai.com"}); err != nil {
		t.Fatalf("ReplaceAll() error = %v", err)
	}

	list, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(list) != 2 || list[0] != "openai.com" || list[1] != "chatgpt.com" {
		t.Fatalf("unexpected list: %+v", list)
	}

	content, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "openai.com\nchatgpt.com\n" {
		t.Fatalf("unexpected runtime file content: %q", string(content))
	}

	count, err := manager.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
}

func TestManagerMigratesLegacyDomainsFile(t *testing.T) {
	db := openDomainsTestDB(t)
	tempDir := t.TempDir()
	runtimePath := filepath.Join(tempDir, ".vpn-manager", "domains.list")
	legacyPath := filepath.Join(tempDir, "domains.list")

	if err := os.WriteFile(legacyPath, []byte("example.com\nchatgpt.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manager := NewManager(db, runtimePath, legacyPath)
	list, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(list) != 2 || list[0] != "example.com" || list[1] != "chatgpt.com" {
		t.Fatalf("unexpected migrated list: %+v", list)
	}
}

func TestNormalizeEntriesSanitizesImportedDomains(t *testing.T) {
	got := NormalizeEntries([]string{
		"# comment only",
		"https://www.canva.com/design?id=1",
		"*.auth.openai.com",
		"cdn*.telegram.org",
		".openai.com",
		"rc.bwa.to/xds",
		"api.anthropic.com",
		"api.anthropic.com # duplicate",
		"***",
	})

	want := []string{
		"www.canva.com",
		"auth.openai.com",
		"telegram.org",
		"openai.com",
		"rc.bwa.to",
		"api.anthropic.com",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeEntries() = %+v, want %+v", got, want)
	}
}

func TestNormalizeEntriesAcceptsIPv4AndCIDR(t *testing.T) {
	got := NormalizeEntries([]string{
		"149.154.167.41",
		"149.154.167.41:443",
		"149.154.160.0/20",
		"149.154.160.0/33",
		"300.1.1.1",
		"91.108.56.0/22",
	})

	want := []string{
		"149.154.167.41",
		"149.154.160.0/20",
		"91.108.56.0/22",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeEntries() = %+v, want %+v", got, want)
	}
}

func TestSplitInputIgnoresCommentsAndSeparators(t *testing.T) {
	raw := `
# comment
youtube.com
googlevideo.com, ytimg.com
cdn*.telegram.org ; *.oaistatic.com
rc.bwa.to/xds
`

	got := SplitInput(raw)
	want := []string{
		"youtube.com",
		"googlevideo.com",
		"ytimg.com",
		"telegram.org",
		"oaistatic.com",
		"rc.bwa.to",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitInput() = %+v, want %+v", got, want)
	}
}

func TestManagerCountDomainsIgnoresIPv4Entries(t *testing.T) {
	db := openDomainsTestDB(t)
	manager := NewManager(db, filepath.Join(t.TempDir(), ".vpn-manager", "domains.list"), filepath.Join(t.TempDir(), "domains.list"))

	if err := manager.ReplaceAll([]string{"youtube.com", "149.154.160.0/20", "91.108.56.130"}); err != nil {
		t.Fatalf("ReplaceAll() error = %v", err)
	}

	total, err := manager.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if total != 3 {
		t.Fatalf("expected total count 3, got %d", total)
	}

	domainCount, err := manager.CountDomains()
	if err != nil {
		t.Fatalf("CountDomains() error = %v", err)
	}
	if domainCount != 1 {
		t.Fatalf("expected domain count 1, got %d", domainCount)
	}
}

func openDomainsTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "vpn-manager.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
