package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"

	"github.com/clas/nanoflare/internal/deployupload"
	"github.com/clas/nanoflare/internal/nanoflare"
)

type deploymentSource struct {
	name        string
	contentType string
	diskPath    string
	content     string
	size        int64
}

// Deployment files stay on disk until the HTTP transport asks for their bytes.
// In-memory esbuild output is the sole exception.
func prepareDeploymentFiles(project Project) ([]nanoflare.WorkerFile, []nanoflare.AssetFile, []deploymentSource, error) {
	var files []nanoflare.WorkerFile
	var assets []nanoflare.AssetFile
	var sources []deploymentSource
	if project.bundledFiles != nil {
		for i, file := range project.bundledFiles {
			files = append(files, nanoflare.WorkerFile{Path: file.Path})
			sources = append(sources, deploymentSource{name: fmt.Sprintf("file-%d", i), contentType: "application/javascript", content: file.Content, size: int64(len(file.Content))})
		}
	} else {
		for i, path := range project.Files {
			clean := filepath.Clean(filepath.FromSlash(path))
			if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, nil, nil, fmt.Errorf("worker file %q must remain inside the project", path)
			}
			info, err := os.Lstat(clean)
			if err != nil {
				return nil, nil, nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, nil, nil, fmt.Errorf("worker file %q must be a regular file", path)
			}
			kind := "application/javascript"
			if filepath.Ext(clean) == ".wasm" {
				kind = "application/wasm"
			}
			files = append(files, nanoflare.WorkerFile{Path: filepath.ToSlash(clean)})
			sources = append(sources, deploymentSource{name: fmt.Sprintf("file-%d", i), contentType: kind, diskPath: clean, size: info.Size()})
		}
	}
	if project.Assets.Directory != "" {
		root := filepath.Clean(filepath.FromSlash(project.Assets.Directory))
		if root == "." || filepath.IsAbs(root) || root == ".." || strings.HasPrefix(root, ".."+string(filepath.Separator)) {
			return nil, nil, nil, fmt.Errorf("asset directory must remain inside the project")
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("asset path %q must not be a symlink", path)
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("asset path %q must be a regular file", path)
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			kind := detectContentType(relative)
			sources = append(sources, deploymentSource{name: fmt.Sprintf("asset-%d", len(assets)), contentType: kind, diskPath: path, size: info.Size()})
			assets = append(assets, nanoflare.AssetFile{Path: relative, ContentType: kind})
			return nil
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if len(sources) > deployupload.MaxFiles {
		return nil, nil, nil, fmt.Errorf("deployment exceeds %d files", deployupload.MaxFiles)
	}
	return files, assets, sources, nil
}

func (r *Runner) requestDeployment(target string, input nanoflare.DeployInput, sources []deploymentSource, output any) error {
	metadata, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if len(metadata) > deployupload.MaxMetadataBytes {
		return fmt.Errorf("deployment metadata exceeds %d bytes", deployupload.MaxMetadataBytes)
	}
	return r.requestWithSender(http.MethodPost, target, output, func(auth AuthConfig) (*http.Response, error) {
		scoped, err := organizationScopedURL(target, auth.ActiveOrgID)
		if err != nil {
			return nil, err
		}
		body, contentType := streamDeployment(metadata, sources)
		defer body.Close()
		request, err := http.NewRequest(http.MethodPost, scoped, body)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", contentType)
		if auth.Token != "" {
			request.Header.Set("Authorization", "Bearer "+auth.Token)
		}
		return r.Client.Do(request)
	})
}

func streamDeployment(metadata []byte, sources []deploymentSource) (io.ReadCloser, string) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		err := writeDeployment(multipartWriter, metadata, sources)
		if err == nil {
			err = multipartWriter.Close()
		}
		writer.CloseWithError(err)
	}()
	return reader, multipartWriter.FormDataContentType()
}

func writeDeployment(writer *multipart.Writer, metadata []byte, sources []deploymentSource) error {
	part, err := writer.CreateFormField("metadata")
	if err != nil {
		return err
	}
	if _, err = part.Write(metadata); err != nil {
		return err
	}
	for _, source := range sources {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="blob"`, source.name))
		header.Set("Content-Type", source.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		var input io.ReadCloser = io.NopCloser(strings.NewReader(source.content))
		if source.diskPath != "" {
			input, err = os.Open(source.diskPath)
			if err != nil {
				return err
			}
		}
		n, copyErr := io.Copy(part, io.LimitReader(input, source.size+1))
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != source.size {
			return fmt.Errorf("deployment file %q changed size during upload; rebuild and retry", source.diskPath)
		}
	}
	return nil
}
