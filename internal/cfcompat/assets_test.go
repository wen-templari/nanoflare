package cfcompat

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestAssetWireDecodersRejectAmbiguousInput(t *testing.T) {
	hash := strings.Repeat("a", 32)
	for _, data := range []string{`{"manifest":null}`, `{"manifest":{"/a":{"hash":"` + hash + `"}}}`, `{"manifest":{"/a":{"hash":"` + hash + `","size":null}}}`, `{"manifest":{"/a":{"HASH":"` + hash + `","size":1}}}`, `{"manifest":{"/a":{"hash":"` + hash + `","size":1,"size":2}}}`, `{"manifest":{},"unknown":1}`} {
		if _, err := DecodeAssetManifest([]byte(data)); err == nil {
			t.Fatalf("accepted invalid manifest %s", data)
		}
	}
	valid := uploadPart{hash, "text/plain", base64.StdEncoding.EncodeToString([]byte("hello"))}
	for _, tc := range []struct {
		name  string
		parts []uploadPart
		ok    bool
	}{
		{"valid", []uploadPart{valid}, true},
		{"missing", nil, false},
		{"duplicate", []uploadPart{valid, valid}, false},
		{"bad hash", []uploadPart{{"filename", "text/plain", valid.body}}, false},
		{"bad base64", []uploadPart{{hash, "text/plain", "!not64!"}}, false},
		{"noncanonical base64", []uploadPart{{hash, "text/plain", "Zh=="}}, false},
		{"unsupported null content type", []uploadPart{{hash, "application/null", valid.body}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, body := uploadFixture(t, tc.parts...)
			_, err := DecodeAssetFiles(kind, bytes.NewReader(body))
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
	kind, body := uploadFixture(t, valid)
	for _, malformed := range [][]byte{
		body[:len(body)-10],
		bytes.Replace(body, []byte("Content-Type:"), []byte("Content-Transfer-Encoding: base64\r\nContent-Type:"), 1),
		bytes.Replace(body, []byte("Content-Type:"), []byte("Content-Type: text/html\r\nContent-Type:"), 1),
	} {
		if _, err := DecodeAssetFiles(kind, bytes.NewReader(malformed)); err == nil {
			t.Fatal("accepted malformed asset multipart")
		}
	}
}

func TestCompletedAssetTranslationUsesOnlyResolvedBytes(t *testing.T) {
	for _, metadata := range []string{
		`{"main_module":"worker.js","assets":null}`,
		`{"main_module":"worker.js","assets":{"JWT":"token"}}`,
		`{"main_module":"worker.js","assets":{"jwt":"token","config":{"Binding":"escape"}}}`,
		`{"main_module":"worker.js","assets":{"jwt":"token","config":null}}`,
		`{"main_module":"worker.js","assets":{"jwt":"token"},"keep_assets":true}`,
	} {
		if _, err := DecodeMetadata([]byte(metadata)); err == nil {
			t.Fatalf("accepted asset metadata: %s", metadata)
		}
	}
	kind, body := uploadFixture(t, uploadPart{"metadata", "application/json", `{"main_module":"worker.js","assets":{"jwt":"completion","config":{"html_handling":"none"}},"bindings":[{"name":"ASSETS","type":"assets"}]}`}, uploadPart{"worker.js", "application/javascript+module", "export default {}"})
	upload, err := DecodeUpload(kind, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CompleteUpload(upload, nil, nil, Options{}); err == nil {
		t.Fatal("accepted unverified completion")
	}
	loaded := 0
	result, err := CompleteUpload(upload, nil, nil, Options{LoadCompletedAssets: func() ([]nanoflare.AssetFile, error) {
		loaded++
		return []nanoflare.AssetFile{{Path: "/hello.txt", Data: []byte("hello"), ContentType: "text/plain"}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 1 || result.Deployment.Assets[0].Path != "hello.txt" || result.Deployment.AssetConfig.Binding != "ASSETS" || result.Deployment.AssetConfig.HTMLHandling != "none" {
		t.Fatal("completed asset translation mismatch")
	}
	upload.Metadata.Bindings = nil
	if _, err = CompleteUpload(upload, nil, nil, Options{LoadCompletedAssets: func() ([]nanoflare.AssetFile, error) {
		return []nanoflare.AssetFile{{Path: "/hello.txt", Data: []byte("hello")}}, nil
	}}); err != nil {
		t.Fatal("implicit asset serving rejected:", err)
	}
}
