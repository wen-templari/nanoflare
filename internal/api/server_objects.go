package api

import (
	"errors"
	"io"
	"net/http"
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
	base := "/v1/organizations/{orgID}"
	buckets := base + "/object-storage-buckets"
	s.mux.HandleFunc("GET "+buckets, s.listObjectStorageBuckets)
	s.mux.HandleFunc("GET "+buckets+"/metrics", s.listObjectStorageBucketMetrics)
	s.mux.HandleFunc("POST "+buckets, s.createObjectStorageBucket)
	s.mux.HandleFunc("GET "+buckets+"/{bucketID}", s.getObjectStorageBucket)
	s.mux.HandleFunc("PATCH "+buckets+"/{bucketID}", s.updateObjectStorageBucket)
	s.mux.HandleFunc("DELETE "+buckets+"/{bucketID}", s.deleteObjectStorageBucket)
	s.mux.HandleFunc("GET "+buckets+"/{bucketID}/analytics", s.objectStorageBucketMetrics)
	objects := base + "/workers/{workerID}/object-storage-buckets/{bucketID}/objects"
	s.mux.HandleFunc("GET "+objects, s.workerObjectList)
	s.mux.HandleFunc("GET "+objects+"/{key...}", s.workerObjectGet)
	s.mux.HandleFunc("PUT "+objects+"/{key...}", s.workerObjectPut)
	s.mux.HandleFunc("DELETE "+objects+"/{key...}", s.workerObjectDelete)
	s.mux.HandleFunc("GET /internal/runtime/objects/{key...}", s.runtimeObjectGet)
	s.mux.HandleFunc("HEAD /internal/runtime/objects/{key...}", s.runtimeObjectHead)
	s.mux.HandleFunc("PUT /internal/runtime/objects/{key...}", s.runtimeObjectPut)
	s.mux.HandleFunc("DELETE /internal/runtime/objects/{key...}", s.runtimeObjectDelete)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-upload", s.retiredPresignUpload)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-download", s.presignDownload)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-public-download", s.presignPublicDownload)
	s.mux.HandleFunc("POST /internal/runtime/objects/upload-session", s.objectUploadSession)
	s.mux.HandleFunc("POST /internal/runtime/objects/delete", s.deleteObject)
}

func (s *Server) listObjectStorageBucketMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	metrics, err := s.service.ObjectStorageBucketMetricsListForOrg(controlOrgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) workerObjectList(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "objects:read") {
		return
	}
	objects, err := s.service.WorkerObjectListForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("bucketID"))
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
	object, body, err := s.service.OpenWorkerObjectForOrg(r.Context(), controlOrgID(r), r.PathValue("workerID"), r.PathValue("bucketID"), key)
	if errors.Is(err, nanoflare.ErrObjectNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeObjectStream(w, object, body)
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
	object, err := s.service.WorkerObjectPutForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("bucketID"), key, r.Header.Get("Content-Type"), body)
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
	if err := s.service.WorkerObjectDeleteForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("bucketID"), key); err != nil {
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
		if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
			status = http.StatusPaymentRequired
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
		if errors.Is(err, nanoflare.ErrObjectStorageBucketInUse) || errors.Is(err, nanoflare.ErrObjectTransferConflict) {
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
	metrics, err := s.service.ObjectStorageBucketMetricsTimeseriesForOrg(controlOrgID(r), r.PathValue("bucketID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func runtimeObjectKey(r *http.Request) (string, error) {
	// ServeMux decodes path values once. A second unescape changes literal keys.
	key := r.PathValue("key")
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
	r, release, ok := beginResourceGrant(w, r, s.service, nanoflare.ExecutionObject, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if !ok {
		return
	}
	defer release()

	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if r == nil {
		return
	}

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
	object, body, err := s.service.OpenObjectContext(r.Context(), bearerToken(r), bucketID, key)
	if errors.Is(err, nanoflare.ErrObjectNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectRead(bucketID)
	writeObjectStream(w, object, body)
}

func writeObjectStream(w http.ResponseWriter, object nanoflare.ObjectInfo, body io.ReadCloser) {
	defer body.Close()
	writeRuntimeObjectHeaders(w.Header(), object)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) runtimeObjectHead(w http.ResponseWriter, r *http.Request) {
	r, release, ok := beginResourceGrant(w, r, s.service, nanoflare.ExecutionObject, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if !ok {
		return
	}
	defer release()

	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if r == nil {
		return
	}

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
	object, ok, err := s.service.ObjectHeadContext(r.Context(), bearerToken(r), bucketID, key)
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
	r, release, ok := beginResourceGrant(w, r, s.service, nanoflare.ExecutionObject, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if !ok {
		return
	}
	defer release()

	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if r == nil {
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
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, err := s.service.ObjectPutContext(r.Context(), bearerToken(r), bucketID, key, r.Header.Get("Content-Type"), body)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectWrite(bucketID)
	writeJSON(w, http.StatusOK, object)
}

func (s *Server) runtimeObjectDelete(w http.ResponseWriter, r *http.Request) {
	r, release, ok := beginResourceGrant(w, r, s.service, nanoflare.ExecutionObject, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if !ok {
		return
	}
	defer release()

	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if r == nil {
		return
	}

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
	if err := s.service.DeleteObjectContext(r.Context(), bearerToken(r), bucketID, key); err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectWrite(bucketID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) retiredPresignUpload(w http.ResponseWriter, r *http.Request) {
	if authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID")) == nil {
		return
	}
	writeError(w, http.StatusGone, errors.New("use an object upload session with a declared size"))
}

func (s *Server) presignDownload(w http.ResponseWriter, r *http.Request) {
	s.presignObject(w, r, s.service.PresignDownload)
}

func (s *Server) presignPublicDownload(w http.ResponseWriter, r *http.Request) {
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	r, release, ok := beginResourceGrant(w, r, s.service, nanoflare.ExecutionObject, bucketID)
	if !ok {
		return
	}
	defer release()
	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, bucketID)
	if r == nil {
		return
	}
	var input struct {
		Key string `json:"key"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	url, err := s.service.PresignPublicObjectDownload(r.Context(), bearerToken(r), bucketID, input.Key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (s *Server) objectUploadSession(w http.ResponseWriter, r *http.Request) {
	bucketID, err := runtimeObjectBucketID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	r, release, ok := beginResourceGrant(w, r, s.service, nanoflare.ExecutionObject, bucketID)
	if !ok {
		return
	}
	defer release()
	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, bucketID)
	if r == nil {
		return
	}
	var input struct {
		nanoflare.ObjectTransferInput
		Action string `json:"action"`
		ID     string `json:"id"`
		Part   int    `json:"part"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var result any
	switch input.Action {
	case "create":
		result, err = s.service.CreateObjectTransfer(r.Context(), bearerToken(r), bucketID, input.ObjectTransferInput)
	case "sign-part", "complete", "abort":
		if input.ID == "" {
			writeError(w, http.StatusBadRequest, errors.New("upload session id is required"))
			return
		}
		result, err = s.service.ObjectTransferAction(r.Context(), bearerToken(r), bucketID, input.ID, input.Action, input.Part)
	default:
		writeError(w, http.StatusBadRequest, errors.New("invalid upload session action"))
		return
	}
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) presignObject(w http.ResponseWriter, r *http.Request, presign func(string, string, string) (string, error)) {
	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if r == nil {
		return
	}
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
	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeObjects, r.Header.Get("X-Nanoflare-Object-Bucket-ID"))
	if r == nil {
		return
	}
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
	if err := s.service.DeleteObjectContext(r.Context(), bearerToken(r), input.BucketID, input.Path); err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeObjectWrite(input.BucketID)
	w.WriteHeader(http.StatusNoContent)
}
