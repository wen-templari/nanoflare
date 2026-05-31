package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("PLATFORM_TEST_EXISTING", "shell")
	if err := os.WriteFile(path, []byte("# comment\nPLATFORM_TEST_ONE=one\nexport PLATFORM_TEST_TWO=\"two words\"\nPLATFORM_TEST_THREE='three words'\nPLATFORM_TEST_EXISTING=file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PLATFORM_TEST_ONE"); got != "one" {
		t.Fatalf("PLATFORM_TEST_ONE = %q, want one", got)
	}
	if got := os.Getenv("PLATFORM_TEST_TWO"); got != "two words" {
		t.Fatalf("PLATFORM_TEST_TWO = %q, want two words", got)
	}
	if got := os.Getenv("PLATFORM_TEST_THREE"); got != "three words" {
		t.Fatalf("PLATFORM_TEST_THREE = %q, want three words", got)
	}
	if got := os.Getenv("PLATFORM_TEST_EXISTING"); got != "shell" {
		t.Fatalf("PLATFORM_TEST_EXISTING = %q, want shell override", got)
	}
}

func TestLoadEnvFileAllowsMissingFile(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatal(err)
	}
}
