package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func cfAssetMultipart(t *testing.T, files map[string][]byte) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for hash, data := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+hash+`"; filename="ignored"`)
		header.Set("Content-Type", "text/plain")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(base64.StdEncoding.EncodeToString(data))); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body.Bytes()
}

func TestCloudflareAssetUploadHTTPPublicationAndIsolation(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "assets-http")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "assets@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "assets"})
			must(err)
			writer := &managedRecordingWriter{}
			service := nanoflare.NewServiceWithObjects(repo, writer, newAPIObjectStore())
			service.SetPlatformDispatchEnabled(true)
			codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
			must(err)
			service.SetSecretCodec(codec)
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "staging", CustomerMode: "platform"})
			must(err)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers"
			script := base + "/dispatch/namespaces/staging/scripts/alpha"
			call := func(method, path, token, kind string, body []byte, key, customer string, want int) json.RawMessage {
				t.Helper()
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", kind)
				r.Header.Set("Idempotency-Key", key)
				if !strings.Contains(path, "/assets/upload?") {
					r.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
				}
				if customer != "" {
					r.Header.Set("X-Nanoflare-Customer-ID", customer)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, r)
				if out.Code != want {
					t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
				}
				var envelope struct {
					Success bool            `json:"success"`
					Result  json.RawMessage `json:"result"`
				}
				must(json.Unmarshal(out.Body.Bytes(), &envelope))
				if envelope.Success != (want < 300) {
					t.Fatal("incorrect asset envelope")
				}
				return envelope.Result
			}
			a, b := []byte("hello asset"), []byte("second asset")
			digest := sha256.Sum256(a)
			aHash := hex.EncodeToString(digest[:16])
			digest = sha256.Sum256(b)
			bHash := hex.EncodeToString(digest[:16])
			manifest, err := json.Marshal(map[string]any{"manifest": map[string]nanoflare.AssetUploadEntry{"/hello.txt": {Hash: aHash, Size: int64(len(a))}, "/second.txt": {Hash: bHash, Size: int64(len(b))}}})
			must(err)
			var initial nanoflare.AssetUploadResult
			must(json.Unmarshal(call("POST", script+"/assets-upload-session", session.Token, "application/json", manifest, "", "alice", 200), &initial))
			if _, err = service.NamespaceScript(org.ID, ns.ID, "alpha"); !errors.Is(err, nanoflare.ErrAppNotFound) {
				t.Fatal("session initiation created a Worker")
			}
			kind, body := cfAssetMultipart(t, map[string][]byte{aHash: a})
			call("POST", base+"/assets/upload?base64=true", session.Token, kind, body, "", "", 401)
			call("POST", base+"/assets/upload?base64=false", initial.JWT, kind, body, "", "", 422)
			call("POST", "/client/v4/accounts/foreign/workers/assets/upload?base64=true", initial.JWT, kind, body, "", "", 401)
			call("GET", script, initial.JWT, "", nil, "", "", 401)
			var partial nanoflare.AssetUploadResult
			must(json.Unmarshal(call("POST", base+"/assets/upload?base64=true", initial.JWT, kind, body, "", "", 200), &partial))
			if partial.JWT != initial.JWT || len(partial.Buckets) != 1 || len(partial.Buckets[0]) != 1 {
				t.Fatal("partial upload completed early")
			}
			kind, body = cfMultipart(t, `{"main_module":"worker.js","assets":{"jwt":"`+initial.JWT+`"},"bindings":[{"name":"ASSETS","type":"assets"}]}`, `export default {}`)
			call("PUT", script, session.Token, kind, body, "premature", "alice", 422)
			kind, body = cfAssetMultipart(t, map[string][]byte{bHash: b})
			var completed nanoflare.AssetUploadResult
			must(json.Unmarshal(call("POST", base+"/assets/upload?base64=true", initial.JWT, kind, body, "", "", 201), &completed))
			call("POST", base+"/assets/upload?base64=true", completed.JWT, kind, body, "", "", 401)
			var retried nanoflare.AssetUploadResult
			must(json.Unmarshal(call("POST", base+"/assets/upload?base64=true", initial.JWT, kind, body, "", "", 201), &retried))
			if retried.JWT != completed.JWT {
				t.Fatal("upload retry changed completion")
			}
			metadata := `{"main_module":"worker.js","compatibility_date":"2025-12-10","assets":{"jwt":"` + completed.JWT + `","config":{"html_handling":"none"}},"bindings":[{"name":"ASSETS","type":"assets"},{"name":"TOKEN","type":"secret_text","text":"pinned"}]}`
			kind, body = cfMultipart(t, metadata, `export default {fetch(request,env){return env.ASSETS.fetch(request)}}`)
			call("PUT", strings.TrimSuffix(script, "alpha")+"beta", session.Token, kind, body, "foreign-script", "bob", 422)
			call("PUT", script, session.Token, kind, body, "first", "bob", 422)
			writer.failNext = true
			call("PUT", script, session.Token, kind, body, "failed-publication", "alice", 400)
			failedApp, err := service.NamespaceScript(org.ID, ns.ID, "alpha")
			must(err)
			failedTraffic, err := repo.ActiveDeploymentsForApp(failedApp.ID)
			must(err)
			if len(failedTraffic) != 0 {
				t.Fatal("failed publication activated asset code")
			}
			call("PUT", script, session.Token, kind, body, "failed-publication", "alice", 400)
			call("PUT", script, session.Token, kind, body, "first", "alice", 200)
			app, err := service.NamespaceScript(org.ID, ns.ID, "alpha")
			must(err)
			snapshot, _, err := service.ManagementUploadForOrg(org.ID, app.ID, true)
			must(err)
			if len(snapshot.Deployment.Assets) != 2 || snapshot.Deployment.Assets[0].Path != "hello.txt" || string(snapshot.Deployment.Assets[0].Data) != string(a) || snapshot.Deployment.AssetConfig.HTMLHandling != "none" {
				t.Fatal("completion did not publish the correct native assets")
			}
			first := snapshot.DeploymentID
			artifact, err := repo.GetDeploymentArtifact(app.ID, first)
			must(err)
			storedArtifact, err := json.Marshal(artifact)
			must(err)
			if bytes.Contains(storedArtifact, []byte(completed.JWT)) || bytes.Contains(storedArtifact, []byte(initial.JWT)) {
				t.Fatal("artifact retained staging credential")
			}
			// Once published, rollback and replay use the artifact rather than the
			// staging credential. Expiring staging must not break retained assets.
			payload, err := base64.RawURLEncoding.DecodeString(strings.Split(completed.JWT, ".")[1])
			must(err)
			var claims struct {
				ID string `json:"sid"`
			}
			must(json.Unmarshal(payload, &claims))
			staged, err := repo.GetAssetUploadSession(claims.ID)
			must(err)
			if staged.WorkerID != app.ID {
				t.Fatal("first attachment did not bind Worker identity")
			}
			staged.ExpiresAt = time.Now().Add(-time.Second)
			expected := staged.Revision
			staged.Revision++
			must(repo.SaveAssetUploadSession(staged, expected))
			call("PUT", script, session.Token, kind, body, "first", "alice", 200)
			call("PUT", script, session.Token, kind, body, "expired", "alice", 422)
			kind, body = cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","keep_assets":true,"keep_bindings":["assets","secret_text"]}`, `export default {fetch(){return new Response("second")}}`)
			call("PUT", script, session.Token, kind, body, "second", "alice", 200)
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: first, TrafficPercent: 100}})
			must(err)
			call("PUT", script, session.Token, kind, body, "second", "alice", 200)
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != first {
				t.Fatal("retry undid rollback")
			}
			// Even restoring the session expiry cannot authorize a replacement
			// Worker under the same name once first attachment bound its old ID.
			staged.ExpiresAt = time.Now().Add(time.Hour)
			expected = staged.Revision
			staged.Revision++
			must(repo.SaveAssetUploadSession(staged, expected))
			call("DELETE", script, session.Token, "", nil, "", "alice", 200)
			_, err = service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", CustomerID: "alice", Execution: "dynamic"})
			must(err)
			kind, body = cfMultipart(t, metadata, `export default {}`)
			call("PUT", script, session.Token, kind, body, "recreated", "alice", 422)
		})
	}
}
