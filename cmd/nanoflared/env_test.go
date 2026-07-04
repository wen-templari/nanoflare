package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("NANOFLARE_TEST_EXISTING", "shell")
	if err := os.WriteFile(path, []byte("# comment\nNANOFLARE_TEST_ONE=one\nexport NANOFLARE_TEST_TWO=\"two words\"\nNANOFLARE_TEST_THREE='three words'\nNANOFLARE_TEST_EXISTING=file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("NANOFLARE_TEST_ONE"); got != "one" {
		t.Fatalf("NANOFLARE_TEST_ONE = %q, want one", got)
	}
	if got := os.Getenv("NANOFLARE_TEST_TWO"); got != "two words" {
		t.Fatalf("NANOFLARE_TEST_TWO = %q, want two words", got)
	}
	if got := os.Getenv("NANOFLARE_TEST_THREE"); got != "three words" {
		t.Fatalf("NANOFLARE_TEST_THREE = %q, want three words", got)
	}
	if got := os.Getenv("NANOFLARE_TEST_EXISTING"); got != "shell" {
		t.Fatalf("NANOFLARE_TEST_EXISTING = %q, want shell override", got)
	}
}

func TestLoadEnvFileAllowsMissingFile(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatal(err)
	}
}
