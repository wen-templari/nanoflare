package nanoflare

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestAssetManifestValidation(t *testing.T) {
	entry := AssetUploadEntry{Hash: strings.Repeat("a", 32), Size: 1}
	for _, name := range []string{"/", "relative", "//double", "/a/../b", "/a\\b", "/a?query", "/a#fragment", "/a\x00b", "/a\tb", "/a\nb", "/a/", "/a/./b", "/" + strings.Repeat("x", 1024)} {
		if ValidateAssetManifest(map[string]AssetUploadEntry{name: entry}) == nil {
			t.Fatalf("accepted unsafe path %q", name)
		}
	}
	for _, bad := range []AssetUploadEntry{{Hash: "hash", Size: 0}, {Hash: strings.Repeat("A", 32), Size: 0}, {Hash: entry.Hash, Size: -1}, {Hash: entry.Hash, Size: MaxAssetUploadBytes + 1}} {
		if ValidateAssetManifest(map[string]AssetUploadEntry{"/index.html": bad}) == nil {
			t.Fatalf("accepted bad entry %#v", bad)
		}
	}
	if ValidateAssetManifest(nil) == nil {
		t.Fatal("accepted null manifest")
	}
	if err := ValidateAssetManifest(map[string]AssetUploadEntry{}); err != nil {
		t.Fatal(err)
	}
	if ValidateAssetManifest(map[string]AssetUploadEntry{"/a": entry, "/b": {Hash: entry.Hash, Size: 2}}) == nil {
		t.Fatal("accepted conflicting sizes")
	}
	if ValidateAssetManifest(map[string]AssetUploadEntry{"/a": {Hash: entry.Hash, Size: MaxAssetUploadBytes}, "/b": entry}) == nil {
		t.Fatal("accepted total size overflow")
	}
}

func TestAssetWireHashMatchesDocumentedExamples(t *testing.T) {
	data := []byte("hello")
	raw, extended := assetWireHash(data, "/path/file.html")
	expectedRaw := sha256.Sum256(data)
	expectedExtended := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(data) + "html"))
	if raw != hex.EncodeToString(expectedRaw[:16]) || extended != hex.EncodeToString(expectedExtended[:16]) {
		t.Fatal("asset hash convention mismatch")
	}
}

func TestAssetUploadPartialCompletionAndImmutableRetry(t *testing.T) {
	store := NewStore()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.CreateOrganization(Organization{ID: "org", Name: "org"}))
	service := NewService(store, &recordingWriter{})
	codec, err := NewSecretCodec("0123456789abcdef0123456789abcdef")
	must(err)
	service.SetSecretCodec(codec)
	ns, err := service.CreateDispatchNamespace("org", CreateDispatchNamespaceInput{Name: "assets"})
	must(err)
	a, b := []byte("first"), []byte("second")
	aHash, _ := assetWireHash(a, "/a.txt")
	_, bHash := assetWireHash(b, "/b.css")
	initial, err := service.CreateAssetUploadForOrg("org", ns.ID, "alpha", "", map[string]AssetUploadEntry{"/a.txt": {Hash: aHash, Size: int64(len(a))}, "/b.css": {Hash: bHash, Size: int64(len(b))}})
	must(err)
	partial, err := service.UploadAssetFiles("org", initial.JWT, map[string]AssetUploadBlob{aHash: {Data: a, ContentType: "text/plain"}})
	must(err)
	if partial.JWT != initial.JWT || len(partial.Buckets) != 1 || len(partial.Buckets[0]) != 1 || partial.Buckets[0][0] != bHash {
		t.Fatal("partial upload completed prematurely")
	}
	if _, err = service.UploadAssetFiles("org", initial.JWT, map[string]AssetUploadBlob{aHash: {Data: a, ContentType: "text/html"}}); err != ErrIdempotencyConflict {
		t.Fatal("retry changed stored content type", err)
	}
	complete, err := service.UploadAssetFiles("org", initial.JWT, map[string]AssetUploadBlob{bHash: {Data: b, ContentType: "text/css"}})
	must(err)
	assets, err := service.CompletedAssetsForOrg("org", ns.ID, "alpha", "", complete.JWT)
	must(err)
	if len(assets) != 2 || string(assets[0].Data) != string(a) || string(assets[1].Data) != string(b) {
		t.Fatal("partial uploads lost data")
	}
	if _, err = service.CreateAssetUploadForOrg("org", ns.ID, "alpha", "customer", map[string]AssetUploadEntry{}); err == nil {
		t.Fatal("organization namespace accepted customer identity")
	}
}

func TestInternalSitesSaltedAssetHash(t *testing.T) {
	data := []byte("<h1>hello</h1>")
	digest := sha256.Sum256(append([]byte("site\x00/index.html\x00"), data...))
	hash := hex.EncodeToString(digest[:16])
	if !assetWireHashMatches(data, "/index.html", "site", hash) {
		t.Fatal("upstream hash rejected")
	}
	for _, tc := range []struct {
		data         []byte
		name, script string
	}{
		{[]byte("changed"), "/index.html", "site"}, {data, "/other.html", "site"}, {data, "/index.html", "other"},
	} {
		if assetWireHashMatches(tc.data, tc.name, tc.script, hash) {
			t.Fatal("accepted altered content/scope")
		}
	}
}

func TestInternalSitesManifestBounds(t *testing.T) {
	manifest := map[string]AssetUploadEntry{}
	for i := 0; i < MaxAssetUploadFiles; i++ {
		manifest[fmt.Sprintf("/file-%d.txt", i)] = AssetUploadEntry{Hash: strings.Repeat("a", 32), Size: 0}
	}
	if err := ValidateAssetManifest(manifest); err != nil {
		t.Fatal("1000 empty files rejected", err)
	}
	manifest["/extra.txt"] = AssetUploadEntry{Hash: strings.Repeat("a", 32), Size: 0}
	if ValidateAssetManifest(manifest) == nil {
		t.Fatal("accepted 1001 files")
	}
	manifest = map[string]AssetUploadEntry{"/a.bin": {Hash: strings.Repeat("a", 32), Size: 25 << 20}, "/b.bin": {Hash: strings.Repeat("b", 32), Size: 25 << 20}}
	if err := ValidateAssetManifest(manifest); err != nil {
		t.Fatal("50 MiB manifest rejected", err)
	}
	manifest["/c.txt"] = AssetUploadEntry{Hash: strings.Repeat("c", 32), Size: 1}
	if ValidateAssetManifest(manifest) == nil {
		t.Fatal("accepted more than 50 MiB")
	}
	manifest = map[string]AssetUploadEntry{"/a.bin": {Hash: strings.Repeat("a", 32), Size: (25 << 20) + 1}}
	if ValidateAssetManifest(manifest) == nil {
		t.Fatal("accepted more than 25 MiB in one file")
	}
}
