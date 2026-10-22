package cfcompat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func DecodeAssetManifest(data []byte) (map[string]nanoflare.AssetUploadEntry, error) {
	var input struct {
		Manifest map[string]nanoflare.AssetUploadEntry `json:"manifest"`
	}
	if len(data) > 1<<20 {
		return nil, errors.New("asset manifest exceeds 1 MiB")
	}
	if err := DecodeObject(data, &input, "manifest"); err != nil {
		return nil, err
	}
	var fields struct {
		Manifest map[string]map[string]json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, errors.New("invalid asset manifest")
	}
	for _, entry := range fields.Manifest {
		if len(entry) != 2 {
			return nil, errors.New("asset manifest entries require hash and size")
		}
		if err := exactFields(entry, "hash", "size"); err != nil {
			return nil, err
		}
		for _, value := range entry {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, errors.New("asset manifest entries cannot be null")
			}
		}
	}
	if err := nanoflare.ValidateAssetManifest(input.Manifest); err != nil {
		return nil, err
	}
	return input.Manifest, nil
}

func DecodeAssetFiles(contentType string, body io.Reader) (map[string]nanoflare.AssetUploadBlob, error) {
	const maxEncodedBytes = ((nanoflare.MaxAssetUploadBytes+2)/3)*4 + (256 << 10)
	data, err := io.ReadAll(io.LimitReader(body, maxEncodedBytes+1))
	if err != nil {
		return nil, errors.New("could not read asset upload")
	}
	if len(data) > maxEncodedBytes {
		return nil, errors.New("encoded asset upload exceeds its limit")
	}
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil || kind != "multipart/form-data" || params["boundary"] == "" {
		return nil, errors.New("asset uploads require multipart/form-data")
	}
	reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	files := map[string]nanoflare.AssetUploadBlob{}
	total := 0
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid asset multipart")
		}
		if len(files) >= nanoflare.MaxAssetUploadFiles {
			return nil, errors.New("too many asset parts")
		}
		if len(part.Header.Values("Content-Disposition")) != 1 || len(part.Header.Values("Content-Type")) > 1 || part.Header.Get("Content-Transfer-Encoding") != "" || part.Header.Get("Content-Encoding") != "" {
			return nil, errors.New("ambiguous or encoded asset part")
		}
		name := part.FormName()
		if len(name) != 32 {
			return nil, errors.New("asset parts must use manifest hashes")
		}
		if _, exists := files[name]; exists {
			return nil, errors.New("duplicate asset part")
		}
		encoded, err := io.ReadAll(part)
		if err != nil {
			return nil, errors.New("invalid asset part")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(string(encoded))
		if err != nil {
			return nil, errors.New("asset content requires standard base64")
		}
		total += len(decoded)
		if total > nanoflare.MaxAssetUploadBytes {
			return nil, errors.New("decoded assets exceed 10 MiB")
		}
		content := part.Header.Get("Content-Type")
		if content == "application/null" {
			return nil, errors.New("omitting the served content type is unsupported in this native profile")
		}
		files[name] = nanoflare.AssetUploadBlob{Data: decoded, ContentType: content}
	}
	if len(files) == 0 {
		return nil, errors.New("asset upload requires files")
	}
	return files, nil
}
