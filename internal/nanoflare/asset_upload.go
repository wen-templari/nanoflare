package nanoflare

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zeebo/blake3"
)

var ErrAssetUploadUnavailable = errors.New("asset upload session is unavailable or expired")

const MaxAssetUploadBytes = 50 << 20
const MaxAssetUploadFileBytes = 25 << 20
const MaxAssetUploadFiles = 1000
const MaxAssetUploadSessions = 16

type AssetUploadEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}
type AssetUploadBlob struct {
	Data        []byte `json:"data"`
	ContentType string `json:"content_type"`
	Digest      string `json:"digest"`
}
type AssetUploadSession struct {
	ID          string                      `json:"id"`
	OrgID       string                      `json:"org_id"`
	NamespaceID string                      `json:"namespace_id"`
	ScriptName  string                      `json:"script_name"`
	CustomerID  string                      `json:"customer_id"`
	WorkerID    string                      `json:"worker_id,omitempty"`
	Revision    int64                       `json:"revision"`
	Manifest    map[string]AssetUploadEntry `json:"manifest"`
	Blobs       map[string]AssetUploadBlob  `json:"blobs"`
	CreatedAt   time.Time                   `json:"created_at"`
	ExpiresAt   time.Time                   `json:"expires_at"`
	CompletedAt *time.Time                  `json:"completed_at,omitempty"`
}
type AssetUploadResult struct {
	JWT     string     `json:"jwt,omitempty"`
	Buckets [][]string `json:"buckets"`
}
type assetUploadClaims struct {
	Session string `json:"sid"`
	Purpose string `json:"purpose"`
	Expires int64  `json:"exp"`
}

func ValidateAssetManifest(manifest map[string]AssetUploadEntry) error {
	if manifest == nil || len(manifest) > MaxAssetUploadFiles {
		return errors.New("asset manifest requires at most 1000 files")
	}
	var total int64
	sizes := map[string]int64{}
	for name, entry := range manifest {
		if len(name) > 1024 || name == "/" || !strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") || path.Clean(name) != name || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\\?#") || strings.IndexFunc(name, unicode.IsControl) >= 0 || !utf8.ValidString(name) {
			return errors.New("invalid asset manifest path")
		}
		decoded, err := hex.DecodeString(entry.Hash)
		if err != nil || len(decoded) != 16 || strings.ToLower(entry.Hash) != entry.Hash || entry.Size < 0 || entry.Size > MaxAssetUploadFileBytes {
			return errors.New("invalid asset hash or size")
		}
		if prior, exists := sizes[entry.Hash]; exists && prior != entry.Size {
			return errors.New("inconsistent asset hash sizes")
		}
		sizes[entry.Hash] = entry.Size
		total += entry.Size
		if total > MaxAssetUploadBytes {
			return errors.New("asset manifest exceeds 50 MiB")
		}
	}
	return nil
}

// Sessions reserve bounded storage before accepting bytes. Repository-wide
// deployment locking makes the per-organization count work across service instances.
func (s *Service) CreateAssetUploadForOrg(org, namespace, script, customer string, manifest map[string]AssetUploadEntry) (AssetUploadResult, error) {
	if s.secrets == nil {
		return AssetUploadResult{}, errors.New("asset upload signing is not configured")
	}
	if err := ValidateAssetManifest(manifest); err != nil {
		return AssetUploadResult{}, err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return AssetUploadResult{}, err
	}
	defer release()
	ns, err := s.DispatchNamespaceForOrg(org, namespace)
	if err != nil {
		return AssetUploadResult{}, err
	}
	app, err := s.NamespaceScript(org, namespace, script)
	worker := ""
	if err == nil {
		if app.Platform.Kind != "customer" || (customer != "" && customer != app.Platform.CustomerID) {
			return AssetUploadResult{}, ErrNamespaceGrantDenied
		}
		worker, customer = app.ID, app.Platform.CustomerID
	} else if !errors.Is(err, ErrAppNotFound) {
		return AssetUploadResult{}, err
	}
	if worker == "" {
		// Use the same identity rules as native script creation without creating
		// an undeployed Worker merely to stage asset bytes.
		if err := validateAssetUploadTarget(ns, script, customer); err != nil {
			return AssetUploadResult{}, err
		}
	}
	now := time.Now().UTC()
	count, err := s.store.CountAssetUploadSessions(org, now)
	if err != nil {
		return AssetUploadResult{}, err
	}
	if count >= MaxAssetUploadSessions {
		return AssetUploadResult{}, errors.New("active asset session limit exceeded")
	}
	id, err := randomToken()
	if err != nil {
		return AssetUploadResult{}, err
	}
	session := AssetUploadSession{ID: id, OrgID: org, NamespaceID: namespace, ScriptName: script, CustomerID: customer, WorkerID: worker, Revision: 1, Manifest: manifest, Blobs: map[string]AssetUploadBlob{}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if len(manifest) == 0 {
		session.CompletedAt = &now
	}
	if err = s.store.SaveAssetUploadSession(session, 0); err != nil {
		return AssetUploadResult{}, err
	}
	return s.assetUploadResult(session), nil
}

func (s *Service) assetUploadToken(session AssetUploadSession, purpose string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(assetUploadClaims{Session: session.ID, Purpose: purpose, Expires: session.ExpiresAt.Unix()})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(s.assetUploadSignature(unsigned))
}

func (s *Service) assetUploadSignature(unsigned string) []byte {
	derive := hmac.New(sha256.New, s.secrets.key)
	_, _ = derive.Write([]byte("nanoflare-assets-signing-v1"))
	mac := hmac.New(sha256.New, derive.Sum(nil))
	_, _ = mac.Write([]byte(unsigned))
	return mac.Sum(nil)
}
func (s *Service) assetUploadResult(session AssetUploadSession) AssetUploadResult {
	result := AssetUploadResult{Buckets: [][]string{}}
	purpose := "upload"
	if session.CompletedAt != nil {
		purpose = "complete"
	} else {
		missing := map[string]bool{}
		for _, entry := range session.Manifest {
			if _, exists := session.Blobs[entry.Hash]; !exists {
				missing[entry.Hash] = true
			}
		}
		bucket := []string{}
		for hash := range missing {
			bucket = append(bucket, hash)
		}
		sort.Strings(bucket)
		if len(bucket) > 0 {
			result.Buckets = append(result.Buckets, bucket)
		}
	}
	result.JWT = s.assetUploadToken(session, purpose)
	return result
}
func (s *Service) assetUploadFromToken(org, token, purpose string) (AssetUploadSession, error) {
	if s.secrets == nil || len(token) > 2048 {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, s.assetUploadSignature(parts[0]+"."+parts[1])) {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	var claims assetUploadClaims
	if json.Unmarshal(data, &claims) != nil || claims.Purpose != purpose || time.Now().Unix() >= claims.Expires {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	session, err := s.store.GetAssetUploadSession(claims.Session)
	if err != nil || session.OrgID != org || !session.ExpiresAt.After(time.Now()) {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	if _, err = s.DispatchNamespaceForOrg(org, session.NamespaceID); err != nil {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	app, err := s.NamespaceScript(org, session.NamespaceID, session.ScriptName)
	if session.WorkerID != "" && (err != nil || app.ID != session.WorkerID) {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	if err == nil && (app.Platform.CustomerID != session.CustomerID || app.Platform.Kind != "customer") {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	if err != nil && !errors.Is(err, ErrAppNotFound) {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	return session, nil
}

func (s *Service) ValidateAssetUploadToken(org, token string) error {
	_, err := s.assetUploadFromToken(org, token, "upload")
	return err
}

func assetWireHash(data []byte, name string) (string, string) {
	raw := sha256.Sum256(data)
	extended := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(data) + strings.TrimPrefix(path.Ext(name), ".")))
	return hex.EncodeToString(raw[:16]), hex.EncodeToString(extended[:16])
}

// The upstream Internal Sites template salts hashes with the script and path.
// Keep this wire convention separate from our full internal content digest.
func assetWireHashMatches(data []byte, name, script, hash string) bool {
	raw, extended := assetWireHash(data, name)
	salted := sha256.Sum256(append([]byte(script+"\x00"+name+"\x00"), data...))
	// Wrangler 4.54 uses BLAKE3(base64(bytes) + extension), truncated to 128
	// bits. The stored blob still carries our independent full content digest.
	wrangler := blake3.Sum256([]byte(base64.StdEncoding.EncodeToString(data) + strings.TrimPrefix(path.Ext(name), ".")))
	return hash == raw || hash == extended || hash == hex.EncodeToString(salted[:16]) || hash == hex.EncodeToString(wrangler[:16])
}

func validateAssetUploadTarget(ns DispatchNamespace, script, customer string) error {
	if !platformName.MatchString(script) || (customer != "" && !platformName.MatchString(customer)) {
		return errors.New("invalid script or customer identity")
	}
	if (ns.CustomerMode == "organization" && customer != "") || (ns.CustomerMode == "platform" && customer == "") {
		return errors.New("customer identity does not match namespace mode")
	}
	return nil
}

func (s *Service) UploadAssetFiles(org, token string, files map[string]AssetUploadBlob) (AssetUploadResult, error) {
	if len(files) == 0 || len(files) > MaxAssetUploadFiles {
		return AssetUploadResult{}, errors.New("asset upload requires files")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return AssetUploadResult{}, err
	}
	defer release()
	session, err := s.assetUploadFromToken(org, token, "upload")
	if err != nil {
		return AssetUploadResult{}, err
	}
	var total int
	for hash, blob := range files {
		total += len(blob.Data)
		if total > MaxAssetUploadBytes {
			return AssetUploadResult{}, errors.New("asset upload exceeds 50 MiB")
		}
		if blob.ContentType == "" {
			blob.ContentType = "application/octet-stream"
		}
		if len(blob.ContentType) > 256 || strings.ContainsAny(blob.ContentType, "\r\n") {
			return AssetUploadResult{}, errors.New("invalid asset content type")
		}
		if _, _, err := mime.ParseMediaType(blob.ContentType); err != nil {
			return AssetUploadResult{}, errors.New("invalid asset content type")
		}
		matched := false
		for name, entry := range session.Manifest {
			if entry.Hash != hash {
				continue
			}
			matched = true
			if int64(len(blob.Data)) != entry.Size || !assetWireHashMatches(blob.Data, name, session.ScriptName, hash) {
				return AssetUploadResult{}, errors.New("asset hash or size mismatch")
			}
		}
		if !matched {
			return AssetUploadResult{}, errors.New("asset is not in the manifest")
		}
		if old, exists := session.Blobs[hash]; exists && (!bytes.Equal(old.Data, blob.Data) || old.ContentType != blob.ContentType) {
			return AssetUploadResult{}, ErrIdempotencyConflict
		}
		blob.Digest = hashBytes(blob.Data)
		session.Blobs[hash] = blob
	}
	if session.CompletedAt != nil {
		return s.assetUploadResult(session), nil
	}
	complete := true
	for _, entry := range session.Manifest {
		if _, exists := session.Blobs[entry.Hash]; !exists {
			complete = false
		}
	}
	if complete {
		now := time.Now().UTC()
		session.CompletedAt = &now
		session.ExpiresAt = now.Add(time.Hour)
	}
	expected := session.Revision
	session.Revision++
	if err = s.store.SaveAssetUploadSession(session, expected); err != nil {
		return AssetUploadResult{}, err
	}
	return s.assetUploadResult(session), nil
}

func (s *Service) CompletedAssetsForOrg(org, namespace, script, customer, token string) ([]AssetFile, error) {
	session, err := s.assetUploadFromToken(org, token, "complete")
	if err != nil {
		return nil, err
	}
	if session.CompletedAt == nil || session.NamespaceID != namespace || session.ScriptName != script || session.CustomerID != customer {
		return nil, ErrAssetUploadUnavailable
	}
	files := []AssetFile{}
	for name, entry := range session.Manifest {
		blob, exists := session.Blobs[entry.Hash]
		if !exists || blob.Digest != hashBytes(blob.Data) || int64(len(blob.Data)) != entry.Size || !assetWireHashMatches(blob.Data, name, session.ScriptName, entry.Hash) {
			return nil, ErrAssetUploadUnavailable
		}
		files = append(files, AssetFile{Path: name, Size: entry.Size, ContentType: blob.ContentType, Data: append([]byte{}, blob.Data...)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Called with both publication locks held, after idempotent replay and before
// artifact creation. Binding persists even when later publication fails, so a
// deleted/recreated Worker cannot recover an earlier instance's completion.
func (s *Service) bindCompletedAssetUpload(org, worker, token string, files []AssetFile) error {
	app, err := s.appForOrg(org, worker)
	if err != nil {
		return err
	}
	if app.Platform == nil || app.Platform.Kind != "customer" {
		return ErrAssetUploadUnavailable
	}
	assets, err := s.CompletedAssetsForOrg(org, app.Platform.NamespaceID, app.Platform.ScriptName, app.Platform.CustomerID, token)
	if err != nil {
		return err
	}
	if len(assets) != len(files) {
		return ErrAssetUploadUnavailable
	}
	byPath := map[string]AssetFile{}
	for _, file := range files {
		if _, exists := byPath[file.Path]; exists {
			return ErrAssetUploadUnavailable
		}
		byPath[file.Path] = file
	}
	for _, asset := range assets {
		file, exists := byPath[strings.TrimPrefix(asset.Path, "/")]
		if !exists || file.ObjectKey != "" || file.ContentType != asset.ContentType || !bytes.Equal(file.Data, asset.Data) {
			return ErrAssetUploadUnavailable
		}
	}
	session, err := s.assetUploadFromToken(org, token, "complete")
	if err != nil {
		return err
	}
	if session.WorkerID == "" {
		expected := session.Revision
		session.Revision++
		session.WorkerID = worker
		return s.store.SaveAssetUploadSession(session, expected)
	}
	return nil
}
