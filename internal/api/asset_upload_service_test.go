package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestAssetUploadPersistentScopeIntegrityAndRetry(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "asset-org", Name: "asset-org", UsageLevel: nanoflare.UsageLevelPaid}))
			service, codec := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("asset-org", nanoflare.CreateDispatchNamespaceInput{Name: "assets", CustomerMode: "platform"})
			must(err)
			body := []byte("<p>asset</p>")
			digest := sha256.Sum256(body)
			hash := hex.EncodeToString(digest[:16])
			manifest := map[string]nanoflare.AssetUploadEntry{"/index.html": {Hash: hash, Size: int64(len(body))}, "/copy.html": {Hash: hash, Size: int64(len(body))}}
			created, err := service.CreateAssetUploadForOrg("asset-org", ns.ID, "alpha", "alice", manifest)
			must(err)
			if len(created.Buckets) != 1 || len(created.Buckets[0]) != 1 || created.Buckets[0][0] != hash {
				t.Fatal("duplicate hashes were not grouped")
			}
			if _, err = service.CompletedAssetsForOrg("asset-org", ns.ID, "alpha", "alice", created.JWT); err == nil {
				t.Fatal("upload credential accepted for completion")
			}
			files := map[string]nanoflare.AssetUploadBlob{hash: {Data: body, ContentType: "text/html"}}
			if _, err = service.UploadAssetFiles("other-org", created.JWT, files); err == nil {
				t.Fatal("upload crossed account")
			}
			if _, err = service.UploadAssetFiles("asset-org", created.JWT+"x", files); err == nil {
				t.Fatal("forged token accepted")
			}
			if _, err = service.UploadAssetFiles("asset-org", created.JWT, map[string]nanoflare.AssetUploadBlob{hash: {Data: []byte("wrong")}}); err == nil {
				t.Fatal("bad content accepted")
			}
			completed, err := service.UploadAssetFiles("asset-org", created.JWT, files)
			must(err)
			if len(completed.Buckets) != 0 || completed.JWT == created.JWT {
				t.Fatal("completion credential not distinct")
			}
			// A new Service has no session/token process memory.
			restarted := nanoflare.NewService(repo, discardWriter{})
			restarted.SetSecretCodec(codec)
			replayed, err := restarted.UploadAssetFiles("asset-org", created.JWT, files)
			must(err)
			if replayed.JWT != completed.JWT {
				t.Fatal("upload retry changed completion")
			}
			if _, err = restarted.UploadAssetFiles("asset-org", completed.JWT, files); err == nil {
				t.Fatal("completion token authorized upload")
			}
			for _, target := range []struct{ org, namespace, script, customer string }{{"other-org", ns.ID, "alpha", "alice"}, {"asset-org", ns.ID, "beta", "alice"}, {"asset-org", ns.ID, "alpha", "bob"}, {"asset-org", "foreign", "alpha", "alice"}} {
				if _, err = restarted.CompletedAssetsForOrg(target.org, target.namespace, target.script, target.customer, completed.JWT); err == nil {
					t.Fatal("completion crossed scope")
				}
			}
			assets, err := restarted.CompletedAssetsForOrg("asset-org", ns.ID, "alpha", "alice", completed.JWT)
			must(err)
			if len(assets) != 2 || string(assets[0].Data) != string(body) || assets[0].ContentType != "text/html" {
				t.Fatal("completed content mismatch")
			}
			assets[0].Data[0] = 'x'
			again, err := restarted.CompletedAssetsForOrg("asset-org", ns.ID, "alpha", "alice", completed.JWT)
			must(err)
			if string(again[0].Data) != string(body) {
				t.Fatal("caller mutated persisted content")
			}
			// Recover the public session identifier to inject storage corruption,
			// then verify the full internal digest is checked on attachment.
			payload, err := base64.RawURLEncoding.DecodeString(strings.Split(completed.JWT, ".")[1])
			must(err)
			var claims struct {
				ID string `json:"sid"`
			}
			must(json.Unmarshal(payload, &claims))
			session, err := repo.GetAssetUploadSession(claims.ID)
			must(err)
			blob := session.Blobs[hash]
			blob.Data = []byte("corrupted")
			session.Blobs[hash] = blob
			expected := session.Revision
			session.Revision++
			must(repo.SaveAssetUploadSession(session, expected))
			if _, err = restarted.CompletedAssetsForOrg("asset-org", ns.ID, "alpha", "alice", completed.JWT); err == nil {
				t.Fatal("storage corruption accepted")
			}
			session.ExpiresAt = time.Now().Add(-time.Second)
			expected = session.Revision
			session.Revision++
			must(repo.SaveAssetUploadSession(session, expected))
			if _, err = restarted.UploadAssetFiles("asset-org", created.JWT, files); err == nil {
				t.Fatal("expired session accepted")
			}
			// Capacity reservation remains bounded under concurrent initiations.
			var wg sync.WaitGroup
			var mu sync.Mutex
			success := 0
			for index := range nanoflare.MaxAssetUploadSessions + 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					selected := service
					if index%2 == 0 {
						selected = restarted
					}
					_, err := selected.CreateAssetUploadForOrg("asset-org", ns.ID, "alpha", "alice", manifest)
					if err == nil {
						mu.Lock()
						success++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if success != nanoflare.MaxAssetUploadSessions {
				t.Fatalf("session reservation count %d", success)
			}
		})
	}
}
