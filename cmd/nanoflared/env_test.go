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

func TestGeneratedLitestreamReplicaConfigUsesExplicitPrefix(t *testing.T) {
	t.Setenv("NANOFLARE_LITESTREAM_REPLICA_URL_PREFIX", "s3://backups/nanoflare")
	t.Setenv("NANOFLARE_LITESTREAM_ENDPOINT", "https://s3.example.com")
	t.Setenv("NANOFLARE_LITESTREAM_REGION", "auto")
	t.Setenv("NANOFLARE_LITESTREAM_ACCESS_KEY_ID", "access")
	t.Setenv("NANOFLARE_LITESTREAM_SECRET_ACCESS_KEY", "secret")
	t.Setenv("NANOFLARE_LITESTREAM_FORCE_PATH_STYLE", "true")

	config, err := generatedLitestreamReplicaConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.URLPrefix != "s3://backups/nanoflare" || config.Endpoint != "https://s3.example.com" || config.Region != "auto" {
		t.Fatalf("config = %#v", config)
	}
	if config.AccessKeyID != "access" || config.SecretAccessKey != "secret" || !config.ForcePathStyle {
		t.Fatalf("config = %#v", config)
	}
}

func TestGeneratedLitestreamReplicaConfigFallsBackToMinIO(t *testing.T) {
	t.Setenv("MINIO_ENDPOINT", "minio:9000")
	t.Setenv("MINIO_BUCKET", "nanoflare")
	t.Setenv("MINIO_ACCESS_KEY", "minio-access")
	t.Setenv("MINIO_SECRET_KEY", "minio-secret")
	t.Setenv("MINIO_SECURE", "false")

	config, err := generatedLitestreamReplicaConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.URLPrefix != "s3://nanoflare/litestream" || config.Endpoint != "http://minio:9000" || config.Region != "us-east-1" {
		t.Fatalf("config = %#v", config)
	}
	if config.AccessKeyID != "minio-access" || config.SecretAccessKey != "minio-secret" || !config.ForcePathStyle {
		t.Fatalf("config = %#v", config)
	}
}
