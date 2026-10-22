package cfcompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/textproto"
	"reflect"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type uploadPart struct{ name, kind, body string }

func uploadFixture(t *testing.T, parts ...uploadPart) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+part.name+`"; filename="ignored.js"`)
		header.Set("Content-Type", part.kind)
		dst, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = dst.Write([]byte(part.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body.Bytes()
}
func parseFixture(t *testing.T, metadata string, previous *Previous, resolve Resolver, options Options) (Candidate, error) {
	t.Helper()
	kind, body := uploadFixture(t, uploadPart{"metadata", "application/json", metadata}, uploadPart{"worker.js", "application/javascript+module", `export default {fetch(){return new Response("ok")}}`})
	return ParseUpload(kind, bytes.NewReader(body), previous, resolve, options)
}
func TestUploadProducesEquivalentNativeSnapshot(t *testing.T) {
	metadata := `{"main_module":"worker.js","compatibility_date":"2025-12-10","compatibility_flags":["nodejs_compat"],"bindings":[{"name":"MESSAGE","type":"plain_text","text":"hello"},{"name":"CONFIG","type":"json","json":{"answer":42}},{"name":"STATE","type":"kv_namespace","namespace_id":"kv-id"},{"name":"DB","type":"d1","id":"db-id"},{"name":"FILES","type":"r2_bucket","bucket_name":"uploads"},{"name":"API","type":"service","service":"platform-api","environment":"production"},{"name":"TOKEN","type":"secret_text","text":"never-in-output"}],"tags":["customer-a","production"]}`
	result, err := parseFixture(t, metadata, nil, func(kind, name string) (string, error) {
		switch {
		case kind == "r2_bucket" && name == "uploads":
			return "bucket-id", nil
		case kind == "service" && name == "platform-api":
			return "worker-id", nil
		}
		t.Fatalf("unexpected resolution %s %s", kind, name)
		return "", nil
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", CompatibilityFlags: []string{"nodejs_compat"}, Files: []nanoflare.WorkerFile{{Name: "worker.js", Path: "worker.js", Size: 59, Content: `export default {fetch(){return new Response("ok")}}`}}, Vars: map[string]json.RawMessage{"MESSAGE": json.RawMessage(`"hello"`), "CONFIG": json.RawMessage(`{"answer":42}`)}, KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: "kv-id"}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: "db-id"}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "FILES", BucketID: "bucket-id"}}, Services: []nanoflare.ServiceBinding{{Binding: "API", WorkerID: "worker-id"}}}
	want.Files[0].Size = int64(len(want.Files[0].Content))
	if !reflect.DeepEqual(result.Deployment, want) {
		t.Fatalf("snapshot mismatch\ngot: %#v\nwant: %#v", result.Deployment, want)
	}
	if result.Secrets["TOKEN"] != "never-in-output" || len(result.KeepSecrets) != 0 {
		t.Fatal("secret intent missing")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("never-in-output")) {
		t.Fatal("candidate leaked secret value")
	}
	if !reflect.DeepEqual(result.Tags, []string{"customer-a", "production"}) {
		t.Fatal(result.Tags)
	}
}

func TestPreservationInheritanceAndReplacement(t *testing.T) {
	text := "old"
	previous := &Previous{Tags: []string{"existing"}, Bindings: []Binding{{Name: "MESSAGE", Type: "plain_text", Text: &text}, {Name: "STATE", Type: "kv_namespace", NamespaceID: "old-kv"}, {Name: "DROP", Type: "d1", ID: "drop-db"}, {Name: "TOKEN", Type: "secret_text"}}, Deployment: nanoflare.DeployInput{Assets: []nanoflare.AssetFile{{Path: "/hello.txt", Data: []byte("hello")}}, AssetConfig: nanoflare.AssetConfig{Binding: "OLD_ASSETS", HTMLHandling: "auto-trailing-slash"}}}
	before, _ := json.Marshal(previous)
	result, err := parseFixture(t, `{"main_module":"worker.js","keep_bindings":["plain_text","kv_namespace"],"keep_assets":true,"bindings":[{"name":"STATE","type":"kv_namespace","namespace_id":"replacement"},{"name":"RENAMED_TOKEN","type":"inherit","old_name":"TOKEN","version_id":"latest"},{"name":"ASSETS","type":"assets"}]}`, previous, nil, Options{StrictInherit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deployment.Databases) != 0 || len(result.Deployment.KVNamespaces) != 1 || result.Deployment.KVNamespaces[0].ID != "replacement" || string(result.Deployment.Vars["MESSAGE"]) != `"old"` {
		t.Fatalf("incorrect retention: %#v", result.Deployment)
	}
	if result.KeepSecrets["RENAMED_TOKEN"] != "TOKEN" || len(result.Secrets) != 0 {
		t.Fatal("secret inheritance failed")
	}
	if len(result.Deployment.Assets) != 1 || result.Deployment.AssetConfig.Binding != "ASSETS" || result.Deployment.AssetConfig.HTMLHandling != "auto-trailing-slash" {
		t.Fatal("assets not preserved")
	}
	if result.Tags[0] != "existing" || result.Deployment.CompatibilityDate != "2021-11-02" {
		t.Fatal("incorrect omitted defaults")
	}
	after, _ := json.Marshal(previous)
	if !bytes.Equal(before, after) {
		t.Fatal("previous snapshot mutated")
	}
	result, err = parseFixture(t, `{"main_module":"worker.js","tags":[],"bindings":[{"name":"missing","type":"inherit"}]}`, previous, nil, Options{})
	if err != nil || len(result.Bindings) != 0 || len(result.Tags) != 0 || len(result.Deployment.Assets) != 0 {
		t.Fatalf("omission/replacement semantics: %#v %v", result, err)
	}
	if _, err = parseFixture(t, `{"main_module":"worker.js","bindings":[{"name":"missing","type":"inherit"}]}`, previous, nil, Options{StrictInherit: true}); err == nil {
		t.Fatal("strict unresolved inheritance accepted")
	}
}

func TestUploadRejectsAmbiguousUnsupportedOrUnsafeMetadata(t *testing.T) {
	for name, metadata := range map[string]string{
		"duplicate field":                  `{"main_module":"worker.js","main_module":"other.js"}`,
		"case alias":                       `{"main_module":"worker.js","MAIN_MODULE":"other.js"}`,
		"binding case alias":               `{"main_module":"worker.js","bindings":[{"name":"A","type":"plain_text","text":"one","TEXT":"two"}]}`,
		"duplicate nested field":           `{"main_module":"worker.js","bindings":[{"type":"plain_text","name":"A","text":"x","text":"y"}]}`,
		"classic":                          `{"body_part":"worker.js"}`,
		"cpu intent":                       `{"main_module":"worker.js","limits":{"cpu_ms":10}}`,
		"unsupported protection even null": `{"main_module":"worker.js","limits":null}`,
		"unsupported module":               `{"main_module":"../worker.js"}`,
		"unknown binding":                  `{"main_module":"worker.js","bindings":[{"type":"ai","name":"AI"}]}`,
		"mixed binding fields":             `{"main_module":"worker.js","bindings":[{"type":"kv_namespace","name":"KV","namespace_id":"one","text":"hidden"}]}`,
		"missing resource":                 `{"main_module":"worker.js","bindings":[{"type":"kv_namespace","name":"KV"}]}`,
		"duplicate binding":                `{"main_module":"worker.js","bindings":[{"type":"json","name":"A","json":null},{"type":"plain_text","name":"A","text":"two"}]}`,
		"secret without value":             `{"main_module":"worker.js","bindings":[{"type":"secret_text","name":"TOKEN"}]}`,
		"wrong inherit field":              `{"main_module":"worker.js","bindings":[{"type":"inherit","name":"A","namespace_id":"override"}]}`,
		"inherit version":                  `{"main_module":"worker.js","bindings":[{"type":"inherit","name":"A","version_id":"arbitrary"}]}`,
		"unsupported retained binding":     `{"main_module":"worker.js","keep_bindings":["durable_object_namespace"]}`,
		"service entrypoint":               `{"main_module":"worker.js","bindings":[{"type":"service","name":"API","service":"api","entrypoint":"Admin"}]}`,
		"unavailable assets token":         `{"main_module":"worker.js","assets":{"jwt":"fake"}}`,
		"second json value":                `{"main_module":"worker.js"} {}`,
		"null":                             `null`,
		"missing main":                     `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFixture(t, metadata, nil, nil, Options{}); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}

func TestResourceResolutionFailureDoesNotLeakCredentialOrResource(t *testing.T) {
	_, err := parseFixture(t, `{"main_module":"worker.js","bindings":[{"name":"TOKEN","type":"secret_text","text":"top-secret"},{"name":"FILES","type":"r2_bucket","bucket_name":"foreign-bucket"}]}`, nil, func(string, string) (string, error) { return "", errors.New("foreign account details and top-secret") }, Options{})
	if err == nil || strings.Contains(err.Error(), "top-secret") || strings.Contains(err.Error(), "foreign") {
		t.Fatal("resolution leaked details")
	}
}

func TestMultipartBoundariesAndPartIdentity(t *testing.T) {
	meta := uploadPart{"metadata", "application/json", `{"main_module":"worker.js"}`}
	code := uploadPart{"worker.js", "application/javascript+module", "export default {}"}
	for name, parts := range map[string][]uploadPart{"duplicate module": {meta, code, code}, "duplicate metadata": {meta, meta, code}, "missing main": {meta}, "wrong content type": {meta, {"worker.js", "application/wasm", "binary"}}, "invalid path": {meta, code, {"../escape.js", "application/javascript+module", "x"}}, "invalid utf8": {meta, {"worker.js", "application/javascript+module", string([]byte{0xff})}}} {
		t.Run(name, func(t *testing.T) {
			kind, body := uploadFixture(t, parts...)
			if _, err := ParseUpload(kind, bytes.NewReader(body), nil, nil, Options{}); err == nil {
				t.Fatal("invalid parts accepted")
			}
		})
	}
	if _, err := ParseUpload("application/json", strings.NewReader("{}"), nil, nil, Options{}); err == nil {
		t.Fatal("non multipart accepted")
	}
	if _, err := ParseUpload("multipart/form-data; boundary=x", strings.NewReader(strings.Repeat("x", MaxUploadBytes+1)), nil, nil, Options{}); err == nil {
		t.Fatal("oversized upload accepted")
	}
	kind, body := uploadFixture(t, code, meta, uploadPart{"lib/util.js", "application/javascript+module", "export const answer=42"})
	encoded := bytes.Replace(body, []byte("Content-Type: application/javascript+module\r\n"), []byte("Content-Type: application/javascript+module\r\nContent-Transfer-Encoding: quoted-printable\r\n"), 1)
	if _, err := ParseUpload(kind, bytes.NewReader(encoded), nil, nil, Options{}); err == nil {
		t.Fatal("encoded multipart part accepted")
	}
	result, err := ParseUpload(kind, bytes.NewReader(body), nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deployment.Files) != 2 || result.Deployment.Files[0].Path != "lib/util.js" || result.Deployment.Entrypoint != "worker.js" {
		t.Fatal("module part names/order not preserved")
	}
}

func TestUploadPlainMetadataFieldKeepsExactJSONValidation(t *testing.T) {
	for _, tc := range []struct {
		metadata string
		ok       bool
	}{
		{`{"main_module":"worker.js"}`, true},
		{`{"main_module":"worker.js","MAIN_MODULE":"other.js"}`, false},
		{`{"main_module":"worker.js","main_module":"other.js"}`, false},
		{`{"main_module":"worker.js","limits":{"cpu_ms":1}}`, false},
	} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("metadata", tc.metadata); err != nil {
			t.Fatal(err)
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="worker.js"; filename="worker.js"`)
		header.Set("Content-Type", "application/javascript+module")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("export default {}"))
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = ParseUpload(writer.FormDataContentType(), bytes.NewReader(body.Bytes()), nil, nil, Options{})
		if (err == nil) != tc.ok {
			t.Fatalf("plain metadata accepted=%v want=%v err=%v", err == nil, tc.ok, err)
		}
	}
}
