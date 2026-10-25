package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// Content responses contain source modules only. Metadata, secret snapshots and
// asset storage references must never enter the multipart response.
func (s *Server) cfScriptContent(w http.ResponseWriter, snapshot nanoflare.ManagementUpload) {
	d := snapshot.Deployment
	if d.Format != "modules" || len(d.Files) == 0 {
		cfError(w, 422, "only module content is supported")
		return
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	quote := strings.NewReplacer("\\", "\\\\", "\"", "\\\"")
	found := false
	for _, file := range d.Files {
		if file.Path == "" || strings.ContainsAny(file.Path, "\r\n\x00") {
			cfError(w, 422, "unsupported native module path")
			return
		}
		found = found || (file.Path == d.Entrypoint && file.Wasm == nil)
		header := textproto.MIMEHeader{}
		// Quote even token-safe names. Node 22's FormData parser rejects the
		// otherwise valid unquoted parameters produced by mime.FormatMediaType.
		name := quote.Replace(file.Path)
		header.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+name+`"`)
		data := []byte(file.Content)
		contentType := "application/javascript+module"
		if file.Wasm != nil {
			data = file.Wasm
			contentType = "application/wasm"
		}
		header.Set("Content-Type", contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			cfError(w, 422, "could not encode module content")
			return
		}
		_, _ = part.Write(data)
		if body.Len() > cfcompat.MaxUploadBytes {
			cfError(w, 422, "content exceeds 10 MiB")
			return
		}
	}
	if !found {
		cfError(w, 422, "native main module is missing")
		return
	}
	_ = writer.Close()
	if body.Len() > cfcompat.MaxUploadBytes {
		cfError(w, 422, "content exceeds 10 MiB")
		return
	}
	w.Header().Set("Content-Type", writer.FormDataContentType())
	w.Header().Set("CF-WORKER-MAIN-MODULE-PART", d.Entrypoint)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}
