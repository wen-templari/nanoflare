package cfcompat

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"testing"
)

func TestSDKAssetConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields [][2]string
		ok     bool
	}{
		{"routes before jwt", [][2]string{{"metadata[assets][config][run_worker_first][]", "/api/*"}, {"metadata[assets][config][run_worker_first][]", "/rpc/*"}, {"metadata[assets][config][html_handling]", "auto-trailing-slash"}, {"metadata[assets][jwt]", "token"}}, true},
		{"boolean", [][2]string{{"metadata[assets][jwt]", "token"}, {"metadata[assets][config][run_worker_first]", "true"}}, true},
		{"duplicate jwt", [][2]string{{"metadata[assets][jwt]", "token"}, {"metadata[assets][jwt]", "other"}}, false},
		{"duplicate config", [][2]string{{"metadata[assets][config][html_handling]", "none"}, {"metadata[assets][config][html_handling]", "none"}}, false},
		{"mixed route forms", [][2]string{{"metadata[assets][config][run_worker_first]", "false"}, {"metadata[assets][config][run_worker_first][]", "/api/*"}}, false},
		{"mixed route forms reversed", [][2]string{{"metadata[assets][config][run_worker_first][]", "/api/*"}, {"metadata[assets][config][run_worker_first]", "true"}}, false},
		{"invalid boolean", [][2]string{{"metadata[assets][config][run_worker_first]", "yes"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{"main_module": "worker.js"}
			var err error
			for _, field := range tc.fields {
				if err = addSDKMetadata(fields, field[0], field[1]); err != nil {
					break
				}
			}
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v fields=%v", err, fields)
			}
			if !tc.ok {
				return
			}
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := DecodeMetadata(data)
			if err != nil {
				t.Fatal(err)
			}
			if metadata.Assets.JWT != "token" || metadata.Assets.Config == nil {
				t.Fatalf("lost asset fields: %s", data)
			}
			if tc.name == "routes before jwt" && len(metadata.Assets.Config.RunWorkerFirst.Routes()) != 2 {
				t.Fatalf("lost routes: %s", data)
			}
			if tc.name == "boolean" && !metadata.Assets.Config.RunWorkerFirst.Always() {
				t.Fatalf("lost boolean: %s", data)
			}
		})
	}
}

func TestSDKUploadMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields [][2]string
		ok     bool
	}{
		{"scalar and list", [][2]string{{"metadata[tags][]", "a"}, {"metadata[tags][]", "b"}, {"metadata[keep_assets]", "false"}}, true},
		{"duplicate", [][2]string{{"metadata[main_module]", "other.js"}}, false},
		{"unknown", [][2]string{{"metadata[limits][cpu_ms]", "1"}}, false},
		{"ambiguous bindings", [][2]string{{"metadata[bindings][][name]", "A"}}, false},
		{"invalid bool", [][2]string{{"metadata[keep_assets]", "0"}}, false},
		{"mixed JSON", [][2]string{{"metadata", `{}`}}, false},
		{"invalid UTF8", [][2]string{{"metadata[tags][]", string([]byte{255})}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if err := writer.WriteField("metadata[main_module]", "worker.js"); err != nil {
				t.Fatal(err)
			}
			for _, field := range tc.fields {
				if err := writer.WriteField(field[0], field[1]); err != nil {
					t.Fatal(err)
				}
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="files[]"; filename="worker.js"`)
			h.Set("Content-Type", "application/javascript+module")
			part, err := writer.CreatePart(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("export default {}")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{writer.FormDataContentType(), "application/javascript"} {
				upload, err := DecodeUpload(kind, bytes.NewReader(body.Bytes()))
				if (err == nil) != tc.ok {
					t.Fatalf("%s: upload=%+v error=%v", kind, upload, err)
				}
			}
			if tc.ok {
				for _, malformed := range [][]byte{append([]byte("preamble\r\n"), body.Bytes()...), body.Bytes()[:body.Len()-10]} {
					if _, err := DecodeUpload("application/javascript", bytes.NewReader(malformed)); err == nil {
						t.Fatal("accepted malformed SDK framing")
					}
				}
				if _, err := DecodeUpload("application/javascript; charset=utf-8", bytes.NewReader(body.Bytes())); err == nil {
					t.Fatal("accepted unexpected type parameters")
				}
			}
		})
	}
}
