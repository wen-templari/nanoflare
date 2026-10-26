package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// loadCloudflareBuild uses generated configuration only for artifact locations.
// Source configuration remains authoritative for bindings and platform behavior.
func loadCloudflareBuild(sourcePath string, source Project) (Project, bool, error) {
	root := filepath.Dir(sourcePath)
	pointerPath := filepath.Join(root, ".wrangler", "deploy", "config.json")
	content, err := os.ReadFile(pointerPath)
	if os.IsNotExist(err) {
		return source, false, nil
	}
	if err != nil {
		return source, true, err
	}
	var pointer struct {
		ConfigPath       string            `json:"configPath"`
		AuxiliaryWorkers []json.RawMessage `json:"auxiliaryWorkers"`
	}
	if err := json.Unmarshal(content, &pointer); err != nil {
		return source, true, fmt.Errorf("read Cloudflare build pointer: %w", err)
	}
	if pointer.ConfigPath == "" {
		return source, true, fmt.Errorf("%s: missing configPath; run vite build", pointerPath)
	}
	if len(pointer.AuxiliaryWorkers) != 0 {
		return source, true, fmt.Errorf("Cloudflare builds with auxiliary Workers are not supported yet")
	}
	configPath := pointer.ConfigPath
	if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(filepath.Dir(pointerPath), configPath)
	}
	configPath = filepath.Clean(configPath)
	if _, err := projectBuildPath(root, configPath); err != nil {
		return source, true, err
	}
	content, err = os.ReadFile(configPath)
	if err != nil {
		return source, true, fmt.Errorf("read Cloudflare build output (run vite build): %w", err)
	}
	var built struct {
		Name       string `json:"name"`
		Main       string `json:"main"`
		ConfigPath string `json:"configPath"`
		Assets     struct {
			Directory string `json:"directory"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(content, &built); err != nil {
		return source, true, fmt.Errorf("decode Cloudflare build output: %w", err)
	}
	if built.Name != source.Name || built.Main == "" {
		return source, true, fmt.Errorf("Cloudflare build does not match Worker %q; run vite build", source.Name)
	}
	if built.ConfigPath != "" && filepath.Clean(built.ConfigPath) != filepath.Clean(sourcePath) {
		return source, true, fmt.Errorf("Cloudflare build uses a different source config (%s); rebuild using %s", built.ConfigPath, sourcePath)
	}
	output := filepath.Dir(configPath)
	mainPath := filepath.Join(output, built.Main)
	source.Main, err = projectBuildPath(root, mainPath)
	if err != nil {
		return source, true, err
	}
	if _, err := projectBuildPath(output, mainPath); err != nil {
		return source, true, fmt.Errorf("Cloudflare Worker entry must be inside its output directory: %w", err)
	}
	if ext := filepath.Ext(mainPath); ext != ".js" && ext != ".mjs" {
		return source, true, fmt.Errorf("Cloudflare Worker entry must be a JavaScript module")
	}
	if _, err := os.Stat(mainPath); err != nil {
		return source, true, fmt.Errorf("Cloudflare Worker entry is missing; run vite build: %w", err)
	}
	source.Files = nil
	err = filepath.WalkDir(output, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Cloudflare build contains symlink %s", path)
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && path != output {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs":
			relative, err := projectBuildPath(root, path)
			if err != nil {
				return err
			}
			source.Files = append(source.Files, relative)
		case ".wasm":
			return fmt.Errorf("Cloudflare build contains WebAssembly module %s; binary module uploads are not supported by this deployment path", path)
		}
		return nil
	})
	if err != nil {
		return source, true, err
	}
	source.Format = "modules"
	if built.Assets.Directory != "" {
		source.Assets.Directory, err = projectBuildPath(root, filepath.Join(output, built.Assets.Directory))
		if err != nil {
			return source, true, err
		}
	}
	return source, true, nil
}

func projectBuildPath(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Cloudflare build path %s must remain inside the project", path)
	}
	return filepath.ToSlash(relative), nil
}
