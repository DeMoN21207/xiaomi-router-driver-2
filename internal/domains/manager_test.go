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
