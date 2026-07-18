package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type objectRequest struct {
	Path     string `json:"path"`
	BucketID string `json:"bucket_id,omitempty"`
}

func (s *Server) registerObjectRoutes() {
	s.mux.HandleFunc("GET /v1/object-storage-buckets", s.listObjectStorageBuckets)
	s.mux.HandleFunc("POST /v1/object-storage-buckets", s.createObjectStorageBucket)
	s.mux.HandleFunc("GET /v1/object-storage-buckets/{bucketID}", s.getObjectStorageBucket)
	s.mux.HandleFunc("PATCH /v1/object-storage-buckets/{bucketID}", s.updateObjectStorageBucket)
	s.mux.HandleFunc("DELETE /v1/object-storage-buckets/{bucketID}", s.deleteObjectStorageBucket)
	s.mux.HandleFunc("GET /v1/object-storage-buckets/{bucketID}/metrics", s.objectStorageBucketMetrics)
	s.mux.HandleFunc("GET /v1/apps/{appID}/object-storage-buckets/{bucketID}", s.workerObjectList)
	s.mux.HandleFunc("GET /v1/apps/{appID}/object-storage-buckets/{bucketID}/{key...}", s.workerObjectGet)
	s.mux.HandleFunc("PUT /v1/apps/{appID}/object-storage-buckets/{bucketID}/{key...}", s.workerObjectPut)
	s.mux.HandleFunc("DELETE /v1/apps/{appID}/object-storage-buckets/{bucketID}/{key...}", s.workerObjectDelete)
	s.mux.HandleFunc("GET /internal/runtime/objects/{key...}", s.runtimeObjectGet)
	s.mux.HandleFunc("HEAD /internal/runtime/objects/{key...}", s.runtimeObjectHead)
	s.mux.HandleFunc("PUT /internal/runtime/objects/{key...}", s.runtimeObjectPut)
	s.mux.HandleFunc("DELETE /internal/runtime/objects/{key...}", s.runtimeObjectDelete)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-upload", s.presignUpload)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-download", s.presignDownload)
	s.mux.HandleFunc("POST /internal/runtime/objects/delete", s.deleteObject)
}

func (s *Server) workerObjectList(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	objects, err := s.service.WorkerObjectListForOrg(controlOrgID(r), r.PathValue("appID"), r.PathValue("bucketID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, objects)
}

func (s *Server) workerObjectGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, ok, err := s.service.WorkerObjectGetForOrg(controlOrgID(r), r.PathValue("appID"), r.PathValue("bucketID"), key)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeRuntimeObjectHeaders(w.Header(), object.ObjectInfo)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(object.Body)
}

func (s *Server) workerObjectPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:write") {
		return
	}
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, err := s.service.WorkerObjectPutForOrg(controlOrgID(r), r.PathValue("appID"), r.PathValue("bucketID"), key, r.Header.Get("Content-Type"), body)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, object)
}

func (s *Server) workerObjectDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:write") {
		return
	}
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.WorkerObjectDeleteForOrg(controlOrgID(r), r.PathValue("appID"), r.PathValue("bucketID"), key); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listObjectStorageBuckets(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	buckets, err := s.service.ListObjectStorageBucketsForOrg(controlOrgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, buckets)
}

func (s *Server) createObjectStorageBucket(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:write") {
		return
	}
	var input nanoflare.CreateObjectStorageBucketInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.OrgID = controlOrgID(r)
	if access, ok := controlOAuthAccess(r); ok {
		input.OAuthClientID = access.ClientID
	}
	bucket, err := s.service.CreateObjectStorageBucket(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrObjectStorageBucketExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, bucket)
}

func (s *Server) getObjectStorageBucket(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	bucket, err := s.service.GetObjectStorageBucketForOrg(controlOrgID(r), r.PathValue("bucketID"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, bucket)
}

func (s *Server) updateObjectStorageBucket(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:write") {
		return
	}
	var input nanoflare.UpdateObjectStorageBucketInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bucket, err := s.service.UpdateObjectStorageBucketForOrg(controlOrgID(r), r.PathValue("bucketID"), input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, nanoflare.ErrObjectStorageBucketExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, bucket)
}

func (s *Server) deleteObjectStorageBucket(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:write") {
		return
	}
	err := s.service.DeleteObjectStorageBucketForOrg(controlOrgID(r), r.PathValue("bucketID"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, nanoflare.ErrObjectStorageBucketInUse) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) objectStorageBucketMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	metrics, err := s.service.ObjectStorageBucketMetricsForOrg(controlOrgID(r), r.PathValue("bucketID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func runtimeObjectKey(r *http.Request) (string, error) {
	key, err := url.PathUnescape(r.PathValue("key"))
	if err != nil {
		return "", errors.New("invalid object key encoding")
	}
	if key == "" {
		return "", errors.New("object key cannot be empty")
	}
	if key == "." || key == ".." {
		return "", errors.New(`object keys "." and ".." are not allowed`)
	}
	if strings.Contains(key, "..") {
		return "", errors.New(`object keys must not contain ".."`)
	}
	clean := strings.TrimPrefix(path.Clean("/"+key), "/")
	if clean == "." || clean == "" {
		return "", errors.New("object key cannot be empty")
	}
	return clean, nil
}

func writeRuntimeObjectHeaders(header http.Header, object nanoflare.ObjectInfo) {
	if object.HTTPMetadata.ContentType != "" {
		header.Set("Content-Type", object.HTTPMetadata.ContentType)
	} else {
		header.Set("Content-Type", "application/octet-stream")
	}
	header.Set("Content-Length", strconv.FormatInt(object.Size, 10))
	if object.HTTPETag != "" {
		header.Set("ETag", object.HTTPETag)
	}
	if !object.Uploaded.IsZero() {
		header.Set("Last-Modified", object.Uploaded.UTC().Format(http.TimeFormat))
	}
	header.Set("X-Nanoflare-Object-Key", object.Key)
	header.Set("X-Nanoflare-Object-Etag", object.ETag)
	header.Set("X-Nanoflare-Object-Uploaded", object.Uploaded.UTC().Format(time.RFC3339Nano))
}

func runtimeObjectBucketID(r *http.Request) (string, error) {
	bucketID := strings.TrimSpace(r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if bucketID == "" {
		return "", errors.New("missing object storage bucket id")
	}
	return bucketID, nil
}

func (s *Server) runtimeObjectGet(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, ok, err := s.service.ObjectGet(bearerToken(r), bucketID, key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = s.service.RecordRuntimeObjectRead(bucketID)
	writeRuntimeObjectHeaders(w.Header(), object.ObjectInfo)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(object.Body)
}

func (s *Server) runtimeObjectHead(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, ok, err := s.service.ObjectHead(bearerToken(r), bucketID, key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = s.service.RecordRuntimeObjectRead(bucketID)
	writeRuntimeObjectHeaders(w.Header(), object)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) runtimeObjectPut(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, err := s.service.ObjectPut(bearerToken(r), bucketID, key, r.Header.Get("Content-Type"), body)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectWrite(bucketID)
	writeJSON(w, http.StatusOK, object)
}

func (s *Server) runtimeObjectDelete(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.DeleteObject(bearerToken(r), bucketID, key); err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectWrite(bucketID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) presignUpload(w http.ResponseWriter, r *http.Request) {
	s.presignObject(w, r, s.service.PresignUpload)
}

func (s *Server) presignDownload(w http.ResponseWriter, r *http.Request) {
	s.presignObject(w, r, s.service.PresignDownload)
}

func (s *Server) presignObject(w http.ResponseWriter, r *http.Request, presign func(string, string, string) (string, error)) {
	var input objectRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	if input.BucketID == "" {
		writeError(w, http.StatusBadRequest, errors.New("bucket_id is required"))
		return
	}
	url, err := presign(bearerToken(r), input.BucketID, input.Path)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	var input objectRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	if input.BucketID == "" {
		writeError(w, http.StatusBadRequest, errors.New("bucket_id is required"))
		return
	}
	if err := s.service.DeleteObject(bearerToken(r), input.BucketID, input.Path); err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectWrite(input.BucketID)
	w.WriteHeader(http.StatusNoContent)
}
