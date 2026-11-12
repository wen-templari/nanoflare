package deployupload

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type testPart struct {
	name, kind string
	data       []byte
}

func multipartBody(t *testing.T, input nanoflare.DeployInput, parts ...testPart) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	p, _ := w.CreateFormField("metadata")
	if err := json.NewEncoder(p).Encode(input); err != nil {
		t.Fatal(err)
	}
	for _, item := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+item.name+`"; filename="blob"`)
		h.Set("Content-Type", item.kind)
		p, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), w.FormDataContentType()
}

func TestRawFilesAndBinaryAssets(t *testing.T) {
	wasm := []byte{0, 97, 115, 109, 1, 0, 0, 0, 0, 255, 128}
	asset := []byte{255, 0, 128, 13, 10, 61, 65}
	input := nanoflare.DeployInput{Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js"}, {Path: "lib.wasm"}}, Assets: []nanoflare.AssetFile{{Path: "image.bin", ContentType: "application/octet-stream"}}}
	body, kind := multipartBody(t, input, testPart{"asset-0", "application/octet-stream", asset}, testPart{"file-1", "application/wasm", wasm}, testPart{"file-0", "application/javascript", []byte("export default {}")})
	got, err := Decode(bytes.NewReader(body), kind, nanoflare.DefaultDeploymentLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Files[0].Content != "export default {}" || !bytes.Equal(got.Files[1].Wasm, wasm) || !bytes.Equal(got.Assets[0].Data, asset) {
		t.Fatal("raw file bytes changed")
	}
}

func TestMultipartRejectsInvalidAndOversizedUploads(t *testing.T) {
	limits := nanoflare.DeploymentLimits{WorkerBytes: 10, AssetBytes: 20, RequestBytes: 4096}
	for _, tc := range []struct {
		name     string
		input    nanoflare.DeployInput
		parts    []testPart
		tooLarge bool
	}{
		{"code", nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "a.js"}, {Path: "b.js"}}}, []testPart{{"file-0", "application/javascript", []byte("123456")}, {"file-1", "application/javascript", []byte("12345")}}, true},
		{"asset", nanoflare.DeployInput{Assets: []nanoflare.AssetFile{{Path: "a"}}}, []testPart{{"asset-0", "application/octet-stream", bytes.Repeat([]byte{0}, 21)}}, true},
		{"missing", nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "a.js"}}}, nil, false},
		{"duplicate", nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "a.js"}}}, []testPart{{"file-0", "application/javascript", nil}, {"file-0", "application/javascript", nil}}, false},
		{"unknown", nanoflare.DeployInput{}, []testPart{{"file-0", "application/javascript", nil}}, false},
		{"inline bytes", nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "a.js", Content: "hidden"}}}, nil, false},
		{"storage key", nanoflare.DeployInput{Assets: []nanoflare.AssetFile{{Path: "a", ObjectKey: "other-org"}}}, nil, false},
		{"invalid UTF8", nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "a.js"}}}, []testPart{{"file-0", "application/javascript", []byte{255}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, kind := multipartBody(t, tc.input, tc.parts...)
			_, err := Decode(bytes.NewReader(body), kind, limits)
			if err == nil || errors.Is(err, nanoflare.ErrDeploymentTooLarge) != tc.tooLarge {
				t.Fatalf("error=%v, want tooLarge=%v", err, tc.tooLarge)
			}
		})
	}
}

func TestRequestLimitCountsEpilogueAndJSONTrailingBytes(t *testing.T) {
	limits := nanoflare.DeploymentLimits{WorkerBytes: 10, AssetBytes: 20, RequestBytes: 4096}
	body, kind := multipartBody(t, nanoflare.DeployInput{})
	for _, tc := range []struct {
		body []byte
		kind string
	}{
		{append(body, bytes.Repeat([]byte{' '}, 4096)...), kind},
		{[]byte(`{}` + strings.Repeat(" ", 4096)), "application/json"},
	} {
		if _, err := Decode(bytes.NewReader(tc.body), tc.kind, limits); !errors.Is(err, nanoflare.ErrDeploymentTooLarge) {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestJSONCompatibilityAndStrictParsing(t *testing.T) {
	for _, body := range []string{`{"files":[{"path":"a.js","content":"code"}],"assets":[{"path":"a","data":"AP8="}]}`, `{} {}`, `{"unknown":true}`} {
		got, err := Decode(strings.NewReader(body), "application/json", nanoflare.DefaultDeploymentLimits())
		if strings.HasPrefix(body, `{"files"`) {
			if err != nil || got.Files[0].Content != "code" || !bytes.Equal(got.Assets[0].Data, []byte{0, 255}) {
				t.Fatalf("decode=%v error=%v", got, err)
			}
		} else if err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
