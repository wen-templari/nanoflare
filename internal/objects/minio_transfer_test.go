package objects

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestMinIOPresignedTransfers(t *testing.T) {
	endpoint := os.Getenv("NANOFLARE_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("NANOFLARE_TEST_MINIO_ENDPOINT is not configured")
	}
	ctx := context.Background()
	store, err := Open(ctx, MinIOConfig{
		Endpoint:        endpoint,
		PublicEndpoint:  "http://" + endpoint,
		Bucket:          os.Getenv("NANOFLARE_TEST_MINIO_BUCKET"),
		AccessKey:       os.Getenv("NANOFLARE_TEST_MINIO_ACCESS_KEY"),
		SecretKey:       os.Getenv("NANOFLARE_TEST_MINIO_SECRET_KEY"),
		UploadAccessKey: os.Getenv("NANOFLARE_TEST_MINIO_UPLOAD_ACCESS_KEY"),
		UploadSecretKey: os.Getenv("NANOFLARE_TEST_MINIO_UPLOAD_SECRET_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	request := func(method, url string, body []byte, headers map[string]string) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, data
	}
	first := []byte("first")
	second := []byte("other")
	for _, tc := range []struct {
		name  string
		parts [][]byte
	}{
		{name: "single"},
		{name: "multipart", parts: [][]byte{bytes.Repeat([]byte("p"), 5<<20), second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transfer := nanoflare.ObjectTransfer{ID: tc.name + "-" + time.Now().UTC().Format("150405.000000000"), ExpiresAt: time.Now().Add(time.Minute)}
			destination := "buckets/transfer-test/" + transfer.ID
			t.Cleanup(func() {
				_ = store.CleanupObjectTransfer(ctx, transfer)
				_ = store.Delete(objectTransferScope, destination)
			})
			want := first
			if tc.parts == nil {
				transfer.Size = int64(len(first))
				url, err := store.SignObjectTransfer(ctx, transfer, 0)
				if err != nil {
					t.Fatal(err)
				}
				status, cors, body := request(http.MethodOptions, url, nil, map[string]string{
					"Origin":                         "https://sites.example.test",
					"Access-Control-Request-Method":  "PUT",
					"Access-Control-Request-Headers": "if-none-match",
				})
				if status >= 300 || cors.Get("Access-Control-Allow-Origin") == "" {
					t.Fatalf("browser preflight = %d, %v, %s", status, cors, body)
				}
				headers := map[string]string{"If-None-Match": "*"}
				if status, _, body := request(http.MethodPut, url, first[:2], headers); status < 400 {
					t.Fatalf("changed content length accepted: %d %s", status, body)
				}
				if status, _, body := request(http.MethodPut, url, first, headers); status != http.StatusOK {
					t.Fatalf("upload rejected: %d %s", status, body)
				}
				if status, _, body := request(http.MethodPut, url, second, headers); status != http.StatusPreconditionFailed {
					t.Fatalf("replayed upload = %d %s", status, body)
				}
			} else {
				transfer.UploadID, err = store.CreateMultipartObjectTransfer(ctx, transfer.StagingKey())
				if err != nil {
					t.Fatal(err)
				}
				want = bytes.Join(tc.parts, nil)
				for _, data := range tc.parts {
					transfer.Parts = append(transfer.Parts, int64(len(data)))
					transfer.Size += int64(len(data))
				}
				for i, data := range tc.parts {
					url, err := store.SignObjectTransfer(ctx, transfer, i+1)
					if err != nil {
						t.Fatal(err)
					}
					if status, _, body := request(http.MethodPut, url, data, nil); status != http.StatusOK {
						t.Fatalf("part %d rejected: %d %s", i+1, status, body)
					}
				}
				for range 2 {
					if err := store.CompleteMultipartObjectTransfer(ctx, transfer); err != nil {
						t.Fatal(err)
					}
				}
			}
			for range 2 {
				got, err := store.PublishObjectTransfer(ctx, transfer, destination)
				if err != nil || got.Size != int64(len(want)) {
					t.Fatalf("publish = %#v, %v", got, err)
				}
			}
			url, err := store.PresignPublicDownload(ctx, objectTransferScope, destination, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			status, headers, body := request(http.MethodGet, url, nil, map[string]string{"Origin": "https://sites.example.test"})
			if status != http.StatusOK || !bytes.Equal(body, want) || strings.TrimSpace(headers.Get("Access-Control-Allow-Origin")) == "" {
				t.Fatalf("signed GET = %d, %d bytes, CORS %q", status, len(body), headers.Get("Access-Control-Allow-Origin"))
			}
		})
	}
}
