// Package deployupload implements the native deployment multipart protocol.
package deployupload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const MaxMetadataBytes = 1 << 20
const MaxFiles = 10000

// Decode accepts legacy JSON or metadata-first multipart with raw file parts.
// Each part is named file-N or asset-N, indexing the manifest's files/assets.
func Decode(body io.Reader, contentType string, limits nanoflare.DeploymentLimits) (nanoflare.DeployInput, error) {
	limited := &io.LimitedReader{R: body, N: limits.RequestBytes + 1}
	input, err := decode(limited, contentType, limits)
	if err == nil {
		_, err = io.Copy(io.Discard, limited)
	}
	if limited.N == 0 {
		return input, fmt.Errorf("%w: deployment request exceeds %d bytes", nanoflare.ErrDeploymentTooLarge, limits.RequestBytes)
	}
	if err != nil {
		return input, err
	}
	return input, limits.Validate(input)
}

func decode(body io.Reader, contentType string, limits nanoflare.DeploymentLimits) (nanoflare.DeployInput, error) {
	var input nanoflare.DeployInput
	if contentType == "" {
		contentType = "application/json"
	}
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return input, err
	}
	if kind == "application/json" {
		err = decodeJSON(body, &input)
		return input, err
	}
	if kind != "multipart/form-data" || params["boundary"] == "" {
		return input, errors.New("deployment requires application/json or multipart/form-data")
	}
	reader := multipart.NewReader(body, params["boundary"])
	part, err := reader.NextRawPart()
	if err != nil {
		return input, fmt.Errorf("read deployment metadata: %w", err)
	}
	if part.FormName() != "metadata" {
		return input, errors.New("first deployment part must be metadata")
	}
	metadata, err := readBounded(part, MaxMetadataBytes, "deployment metadata")
	if err != nil {
		return input, err
	}
	if err = decodeJSON(strings.NewReader(string(metadata)), &input); err != nil {
		return input, err
	}
	count := len(input.Files) + len(input.Assets)
	if count > MaxFiles {
		return input, fmt.Errorf("deployment exceeds %d files", MaxFiles)
	}
	for _, file := range input.Files {
		if file.Content != "" || file.Wasm != nil {
			return input, errors.New("multipart metadata must not contain file bytes")
		}
	}
	for _, asset := range input.Assets {
		if asset.Data != nil || asset.ObjectKey != "" {
			return input, errors.New("multipart metadata must not contain asset bytes or object keys")
		}
	}
	seen := make(map[string]bool, count)
	var codeBytes int64
	for {
		part, err = reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return input, err
		}
		if part.Header.Get("Content-Transfer-Encoding") != "" {
			return input, errors.New("deployment parts must contain raw bytes")
		}
		name := part.FormName()
		if seen[name] {
			return input, fmt.Errorf("duplicate deployment part %q", name)
		}
		seen[name] = true
		category, indexText, ok := strings.Cut(name, "-")
		index, parseErr := strconv.Atoi(indexText)
		if !ok || parseErr != nil || index < 0 || strconv.Itoa(index) != indexText {
			return input, errors.New("invalid deployment part name")
		}
		switch category {
		case "file":
			if index >= len(input.Files) {
				return input, errors.New("unknown worker file part")
			}
			data, err := readBounded(part, limits.WorkerBytes-codeBytes, "worker code")
			if errors.Is(err, nanoflare.ErrDeploymentTooLarge) {
				return input, fmt.Errorf("%w: total worker code exceeds %d bytes", nanoflare.ErrDeploymentTooLarge, limits.WorkerBytes)
			}
			if err != nil {
				return input, err
			}
			codeBytes += int64(len(data))
			partType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if err != nil {
				return input, err
			}
			if partType == "application/wasm" {
				input.Files[index].Wasm = data
			} else if partType == "application/javascript" && utf8.Valid(data) {
				input.Files[index].Content = string(data)
			} else {
				return input, errors.New("worker parts require UTF-8 application/javascript or application/wasm")
			}
		case "asset":
			if index >= len(input.Assets) {
				return input, errors.New("unknown asset part")
			}
			data, err := readBounded(part, limits.AssetBytes, fmt.Sprintf("asset %q", input.Assets[index].Path))
			if err != nil {
				return input, err
			}
			input.Assets[index].Data = data
		default:
			return input, errors.New("unknown deployment part")
		}
	}
	if len(seen) != count {
		return input, errors.New("deployment is missing file parts")
	}
	return input, nil
}

func readBounded(reader io.Reader, limit int64, label string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", nanoflare.ErrDeploymentTooLarge, label, limit)
	}
	return data, nil
}

func decodeJSON(reader io.Reader, target *nanoflare.DeployInput) error {
	// Decode through an alias: DeployInput's compatibility UnmarshalJSON method
	// otherwise bypasses DisallowUnknownFields.
	type plain nanoflare.DeployInput
	var value struct {
		plain
		LegacyBuckets []nanoflare.ObjectStorageBucketBinding `json:"object_storage_bucket"`
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*target = nanoflare.DeployInput(value.plain)
	if len(target.ObjectStorageBuckets) == 0 {
		target.ObjectStorageBuckets = value.LegacyBuckets
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("deployment must contain a single JSON object")
	}
	return nil
}
