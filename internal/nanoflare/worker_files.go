package nanoflare

import (
	"bytes"
	"errors"
	"slices"
)

// CloneWorkerFiles detaches binary module storage as well as the file list.
func CloneWorkerFiles(files []WorkerFile) []WorkerFile {
	result := slices.Clone(files)
	for i := range result {
		result[i].Wasm = slices.Clone(result[i].Wasm)
	}
	return result
}

func validateWorkerWasm(file WorkerFile) error {
	if file.Wasm != nil && (file.Content != "" || len(file.Wasm) < 8 || !bytes.Equal(file.Wasm[:8], []byte{0, 97, 115, 109, 1, 0, 0, 0})) {
		return errors.New("Wasm modules require a version 1 binary and no JavaScript content")
	}
	return nil
}
