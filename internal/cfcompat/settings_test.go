package cfcompat

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeSettingsMultipartBoundaries(t *testing.T) {
	valid := uploadPart{"settings", "application/json", `{"tags":["team/a"]}`}
	for _, tc := range []struct {
		name  string
		parts []uploadPart
		ok    bool
	}{
		{"json part", []uploadPart{valid}, true},
		{"string field", []uploadPart{{"settings", "", `{}`}}, true},
		{"missing part", nil, false},
		{"wrong name", []uploadPart{{"metadata", "application/json", `{}`}}, false},
		{"duplicate part", []uploadPart{valid, valid}, false},
		{"extra module", []uploadPart{valid, {"worker.js", "application/javascript", "code"}}, false},
		{"wrong type", []uploadPart{{"settings", "text/plain", `{}`}}, false},
		{"duplicate key", []uploadPart{{"settings", "application/json", `{"tags":[],"tags":["x"]}`}}, false},
		{"unknown field", []uploadPart{{"settings", "application/json", `{"limits":{"cpu_ms":1}}`}}, false},
		{"trailing JSON", []uploadPart{{"settings", "application/json", `{} {}`}}, false},
		{"oversized metadata", []uploadPart{{"settings", "application/json", `{"tags":["` + strings.Repeat("x", 1<<20) + `"]}`}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, body := uploadFixture(t, tc.parts...)
			_, err := DecodeSettings(kind, bytes.NewReader(body))
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
	kind, body := uploadFixture(t, valid)
	for _, tc := range []struct {
		name, kind string
		body       []byte
	}{
		{"missing boundary", "multipart/form-data", body},
		{"wrong outer type", "application/json", body},
		{"truncated body", kind, body[:len(body)-10]},
		{"encoded part", kind, bytes.Replace(body, []byte("Content-Type:"), []byte("Content-Transfer-Encoding: base64\r\nContent-Type:"), 1)},
		{"duplicate type", kind, bytes.Replace(body, []byte("Content-Type:"), []byte("Content-Type: application/json\r\nContent-Type:"), 1)},
		{"oversized framing", kind, bytes.Repeat([]byte("x"), (1<<20)+(64<<10)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeSettings(tc.kind, bytes.NewReader(tc.body)); err == nil {
				t.Fatal("accepted malformed multipart")
			}
		})
	}
	if _, err := DecodeSettings(kind, io.MultiReader(bytes.NewReader(body[:10]), settingsReadFailure{})); err == nil {
		t.Fatal("ignored reader failure")
	}
}

type settingsReadFailure struct{}

func (settingsReadFailure) Read([]byte) (int, error) { return 0, errors.New("test read failure") }

func TestContentUploadRejectsConfigurationMetadata(t *testing.T) {
	for _, metadata := range []string{
		`{"main_module":"worker.js","bindings":[]}`,
		`{"main_module":"worker.js","tags":[]}`,
		`{"main_module":"worker.js","compatibility_date":"2025-12-10"}`,
		`{"body_part":"worker.js"}`,
		`{"main_module":"worker.js","MAIN_MODULE":"worker.js"}`,
	} {
		kind, body := uploadFixture(t, uploadPart{"metadata", "application/json", metadata}, uploadPart{"worker.js", "text/javascript+module", "export default {}"})
		if _, err := DecodeContentUpload(kind, bytes.NewReader(body)); err == nil {
			t.Fatalf("accepted content settings: %s", metadata)
		}
	}
	kind, body := uploadFixture(t, uploadPart{"metadata", "application/json", `{"main_module":"worker.js"}`}, uploadPart{"worker.js", "text/javascript+module", "export default {}"})
	if _, err := DecodeContentUpload(kind, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
}
