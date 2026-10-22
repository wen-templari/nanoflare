package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

func TestPostgresAssetUploadSurvivesConnectionReopen(t *testing.T) {
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := sql.Open("postgres", url)
	must(err)
	defer admin.Close()
	schema := fmt.Sprintf("assets_reopen_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema))
	must(err)
	defer admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
	dsn, err := pq.ParseURL(url)
	must(err)
	dsn += " search_path=" + schema
	repo, err := Open(context.Background(), dsn)
	must(err)
	defer repo.Close()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	must(err)
	service := nanoflare.NewService(repo, managementDiscardWriter{})
	service.SetSecretCodec(codec)
	ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "assets"})
	must(err)
	data := []byte("persistent bytes")
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:16])
	initial, err := service.CreateAssetUploadForOrg("org", ns.ID, "alpha", "", map[string]nanoflare.AssetUploadEntry{"/file.txt": {Hash: hash, Size: int64(len(data))}})
	must(err)
	must(repo.Close())
	reopened, err := Open(context.Background(), dsn)
	must(err)
	defer reopened.Close()
	restarted := nanoflare.NewService(reopened, managementDiscardWriter{})
	restarted.SetSecretCodec(codec)
	completed, err := restarted.UploadAssetFiles("org", initial.JWT, map[string]nanoflare.AssetUploadBlob{hash: {Data: data, ContentType: "text/plain"}})
	must(err)
	must(reopened.Close())
	finalRepo, err := Open(context.Background(), dsn)
	must(err)
	defer finalRepo.Close()
	finalService := nanoflare.NewService(finalRepo, managementDiscardWriter{})
	finalService.SetSecretCodec(codec)
	files, err := finalService.CompletedAssetsForOrg("org", ns.ID, "alpha", "", completed.JWT)
	must(err)
	if len(files) != 1 || string(files[0].Data) != string(data) || files[0].ContentType != "text/plain" {
		t.Fatal("asset session lost across database reopen")
	}
	must(finalService.DeleteDispatchNamespace("org", ns.ID))
	if _, err = finalService.CompletedAssetsForOrg("org", ns.ID, "alpha", "", completed.JWT); err == nil {
		t.Fatal("deleted namespace retained completion authority")
	}
	var count int
	must(finalRepo.db.QueryRow(`SELECT count(*) FROM asset_upload_sessions`).Scan(&count))
	if count != 0 {
		t.Fatal("namespace deletion did not cascade staged assets")
	}
}
