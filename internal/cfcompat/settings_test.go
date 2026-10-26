package cfcompat

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"strings"
	"testing"
)

func TestSDKSettingsFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields [][2]string
		ok     bool
	}{
		{"lists", [][2]string{{"settings[tags][]", "one"}, {"settings[tags][]", "two"}, {"settings[compatibility_date]", "2025-12-10"}, {"settings[compatibility_flags][]", "nodejs_compat"}}, true},
		{"duplicate scalar", [][2]string{{"settings[compatibility_date]", "2025-12-10"}, {"settings[compatibility_date]", "2025-12-11"}}, false},
		{"mixed JSON", [][2]string{{"settings[tags][]", "one"}, {"settings", `{}`}}, false},
		{"unknown", [][2]string{{"settings[limits][cpu_ms]", "1"}}, false},
		{"ambiguous binding", [][2]string{{"settings[bindings][][name]", "A"}}, false},
		{"invalid UTF8", [][2]string{{"settings[tags][]", string([]byte{255})}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for _, field := range tc.fields {
				if err := writer.WriteField(field[0], field[1]); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := DecodeSettings(writer.FormDataContentType(), &body)
			if (err == nil) != tc.ok {
				t.Fatalf("data=%s error=%v", data, err)
			}
			if tc.ok && string(data) != `{"compatibility_date":"2025-12-10","compatibility_flags":["nodejs_compat"],"tags":["one","two"]}` {
				t.Fatalf("decoded %s", data)
			}
		})
	}
}

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
