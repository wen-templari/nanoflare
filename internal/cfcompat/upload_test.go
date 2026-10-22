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

func TestD1DatabaseIdentifierCompatibility(t *testing.T) {
	for _, fields := range []string{`"database_id":"db-id"`, `"id":"db-id"`, `"database_id":"db-id","id":"db-id"`} {
		t.Run(fields, func(t *testing.T) {
			candidate, err := parseFixture(t, `{"main_module":"worker.js","bindings":[{"name":"DB","type":"d1",`+fields+`}]}`, nil, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(candidate.Deployment.Databases, []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: "db-id"}}) || !reflect.DeepEqual(candidate.Bindings, []Binding{{Name: "DB", Type: "d1", DatabaseID: "db-id"}}) {
				t.Fatalf("incorrect D1 translation: %#v", candidate)
			}
		})
	}
	for _, binding := range []string{
		`{"name":"DB","type":"d1","database_id":"db-id","id":"other"}`,
		`{"name":"DB","type":"d1","database_id":""}`,
		`{"name":"DB","type":"d1","DATABASE_ID":"db-id"}`,
		`{"name":"STATE","type":"kv_namespace","namespace_id":"kv-id","database_id":"db-id"}`,
		`{"name":"DB","type":"inherit","database_id":"db-id"}`,
	} {
		if _, err := parseFixture(t, `{"main_module":"worker.js","bindings":[`+binding+`]}`, nil, nil, Options{}); err == nil {
			t.Fatalf("accepted invalid database identifier: %s", binding)
		}
	}
	previous := &Previous{Deployment: nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js"}, Bindings: []Binding{{Name: "DB", Type: "d1", ID: "legacy-db"}}}
	for _, settings := range []string{`{}`, `{"bindings":[{"name":"RENAMED_DB","type":"inherit","old_name":"DB"}]}`, `{"bindings":[{"name":"DB","type":"d1","database_id":"new-db"}]}`} {
		candidate, err := ApplySettings([]byte(settings), previous, nil)
		if err != nil {
			t.Fatal(err)
		}
		wantID, wantName := "legacy-db", "DB"
		if strings.Contains(settings, "RENAMED_DB") {
			wantName = "RENAMED_DB"
		}
		if strings.Contains(settings, "new-db") {
			wantID = "new-db"
		}
		if !reflect.DeepEqual(candidate.Bindings, []Binding{{Name: wantName, Type: "d1", DatabaseID: wantID}}) || candidate.Deployment.Databases[0].DatabaseID != wantID {
			t.Fatalf("incorrect retained/settings D1 binding: %#v", candidate)
		}
	}
	if previous.Bindings[0].ID != "legacy-db" || previous.Bindings[0].DatabaseID != "" {
		t.Fatal("legacy snapshot mutated")
	}
	if _, err := ApplySettings([]byte(`{"bindings":[{"name":"DB","type":"d1","database_id":"new-db","id":"other"}]}`), previous, nil); err == nil {
		t.Fatal("settings accepted conflicting D1 identifiers")
	}
}

func TestOrdinaryDispatchNamespaceBinding(t *testing.T) {
	metadata := `{"main_module":"worker.js","bindings":[{"name":"DISPATCHER","type":"dispatch_namespace","namespace":"sites"}]}`
	kind, body := uploadFixture(t, uploadPart{"metadata", "application/json", metadata}, uploadPart{"worker.js", "application/javascript+module", `export default {}`})
	upload, err := DecodeUpload(kind, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(kind, name string) (string, error) {
		if kind != "dispatch_namespace" || name != "sites" {
			t.Fatalf("unexpected resolver request %s %s", kind, name)
		}
		return "namespace-id", nil
	}
	candidate, err := CompleteUpload(upload, nil, resolve, Options{AllowDispatchNamespaces: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(candidate.Deployment.DispatchNamespaces, []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: "namespace-id"}}) {
		t.Fatalf("incorrect namespace binding: %#v", candidate.Deployment)
	}
	previous := &Previous{Bindings: candidate.Bindings, Deployment: candidate.Deployment}
	for _, next := range []Metadata{
		{MainModule: "worker.js", KeepBindings: []string{"dispatch_namespace"}},
		{MainModule: "worker.js", Bindings: []Binding{{Name: "RENAMED", Type: "inherit", OldName: "DISPATCHER"}}},
	} {
		if _, err := translate(next, previous, resolve, true); err != nil {
			t.Fatal(err)
		}
		if _, err := translate(next, previous, func(string, string) (string, error) { return "replacement-namespace", nil }, true); err == nil {
			t.Fatal("retained namespace silently rebound to a new identity")
		}
		if _, err := Translate(next, previous, resolve); err == nil {
			t.Fatal("customer translation inherited namespace authority")
		}
	}
	if _, err := CompleteUpload(upload, nil, resolve, Options{}); err == nil {
		t.Fatal("customer upload accepted dispatch_namespace")
	}
	if _, err := CompleteUpload(upload, nil, func(string, string) (string, error) { return "", errors.New("private namespace credential") }, Options{AllowDispatchNamespaces: true}); err == nil || strings.Contains(err.Error(), "credential") {
		t.Fatalf("namespace resolution error was missing or leaked: %v", err)
	}
	for _, fields := range []string{`"namespace":""`, `"namespace":"sites","service":"other"`, `"namespace":"sites","outbound":{"service":"other"}`} {
		_, err := parseFixture(t, `{"main_module":"worker.js","bindings":[{"name":"D","type":"dispatch_namespace",`+fields+`}]}`, nil, resolve, Options{AllowDispatchNamespaces: true})
		if err == nil {
			t.Fatalf("accepted unsupported dispatch configuration: %s", fields)
		}
	}
}

// SDK 5.2.0 (the upstream publisher's locked version) uses filename-keyed files.
func TestSDK5FilenameModules(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		wantError      bool
	}{
		{"valid", "worker.js", false}, {"traversal", "../worker.js", true},
		{"metadata", "metadata", true}, {"duplicate", "worker.js", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			field, _ := writer.CreateFormField("metadata")
			field.Write([]byte(`{"main_module":"worker.js"}`))
			for i := 0; i < 1+map[bool]int{true: 1}[tc.name == "duplicate"]; i++ {
				h := textproto.MIMEHeader{}
				h.Set("Content-Disposition", `form-data; name="files"; filename="`+tc.filename+`"`)
				h.Set("Content-Type", "application/javascript+module")
				part, _ := writer.CreatePart(h)
				part.Write([]byte("export default {}"))
			}
			writer.Close()
			upload, err := DecodeUpload(writer.FormDataContentType(), &body)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if err == nil && (len(upload.Files) != 1 || upload.Files[0].Path != "worker.js") {
				t.Fatal(upload)
			}
		})
	}
}
