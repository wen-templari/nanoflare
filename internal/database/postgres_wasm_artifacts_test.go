package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

func TestPostgresWasmArtifactsSurviveReopenAndTrafficRollback(t *testing.T) {
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("disposable PostgreSQL required")
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
	schema := fmt.Sprintf("wasm_reopen_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema))
	must(err)
	defer admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
	dsn, err := pq.ParseURL(url)
	must(err)
	dsn += " search_path=" + schema
	var repo *Postgres
	defer func() {
		if repo != nil {
			repo.Close()
		}
	}()
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	must(err)
	open := func() *nanoflare.Service {
		t.Helper()
		if repo != nil {
			must(repo.Close())
		}
		repo, err = Open(context.Background(), dsn)
		must(err)
		service := nanoflare.NewService(repo, managementDiscardWriter{})
		service.SetSecretCodec(codec)
		return service
	}
	binary := func(answer byte) []byte {
		value, err := hex.DecodeString(fmt.Sprintf("0061736d010000000105016000017f030201000707010372756e00000a0601040041%02x0b00040178fffe", answer))
		must(err)
		return value
	}
	firstBytes, secondBytes := binary(42), binary(7)
	service := open()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "wasm-org", Name: "wasm-org", UsageLevel: nanoflare.UsageLevelPaid}))
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "wasm-org", Name: "wasm-reopen", Hostname: "wasm-reopen.example.test"})
	must(err)
	input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "wasm-first", Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import module from "./answer.wasm";export default {fetch(){return new Response(String(new WebAssembly.Instance(module).exports.run()))}}`}, {Path: "answer.wasm", Wasm: firstBytes}}}, Metadata: nanoflare.ScriptManagementMetadata{Tags: []string{"first"}, Bindings: []json.RawMessage{json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}}, Secrets: map[string]string{"TOKEN": "wasm-persisted-secret"}}
	first, err := service.ManagedDeployForOrg("wasm-org", app.ID, input)
	must(err)
	checkFiles := func(files []nanoflare.WorkerFile, want []byte) {
		t.Helper()
		if len(files) != 2 || files[1].Path != "answer.wasm" || files[1].Content != "" || !bytes.Equal(files[1].Wasm, want) || files[1].Size != int64(len(want)) {
			t.Fatal("binary snapshot changed", files)
		}
	}
	service = open()
	files, err := service.WorkerFilesForOrg("wasm-org", app.ID)
	must(err)
	checkFiles(files, firstBytes)
	old, err := repo.GetDeploymentArtifact(app.ID, first.ID)
	must(err)
	checkFiles(old.Deployment.Files, firstBytes)
	firstDigest := old.Digest
	input.ExpectedRevision = 1
	input.BaseDeploymentID = first.ID
	input.Deployment.IdempotencyKey = "wasm-second"
	input.Deployment.Files[1].Wasm = secondBytes
	input.Metadata.Tags = []string{"second"}
	input.Secrets = nil
	input.KeepSecrets = map[string]string{"TOKEN": "TOKEN"}
	second, err := service.ManagedDeployForOrg("wasm-org", app.ID, input)
	must(err)
	service = open()
	files, err = service.WorkerFilesForOrg("wasm-org", app.ID)
	must(err)
	checkFiles(files, secondBytes)
	_, err = service.SetDeploymentTrafficForOrg("wasm-org", app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
	must(err)
	service = open()
	files, err = service.WorkerFilesForOrg("wasm-org", app.ID)
	must(err)
	checkFiles(files, firstBytes)
	head, found, err := service.ManagementUploadForOrg("wasm-org", app.ID, false)
	must(err)
	if !found || head.DeploymentID != second.ID || head.State.Revision != 2 || len(head.State.Metadata.Tags) != 1 || head.State.Metadata.Tags[0] != "second" {
		t.Fatal("traffic rollback rewrote management head", head)
	}
	checkFiles(head.Deployment.Files, secondBytes)
	for _, expected := range []struct {
		id   string
		wasm []byte
	}{{first.ID, firstBytes}, {second.ID, secondBytes}} {
		artifact, err := repo.GetDeploymentArtifact(app.ID, expected.id)
		must(err)
		checkFiles(artifact.Deployment.Files, expected.wasm)
		if expected.id == first.ID && artifact.Digest != firstDigest {
			t.Fatal("immutable binary artifact changed")
		}
		if len(artifact.Secrets) != 1 {
			t.Fatal("binary publication lost sealed secret")
		}
		value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
		must(err)
		if value != "wasm-persisted-secret" {
			t.Fatal("binary publication lost inherited secret")
		}
	}
	t.Log("postgres_binary_bytes=true non_utf8_custom_section=true pool_reopened_three_times=true binary_update=true traffic_rollback=true management_head_preserved=true inherited_secret=true")
}
