package api

import (
	"io"
	"os"
)

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

func saveUploadedFile(path string, src io.Reader) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}
