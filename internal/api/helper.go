package api

import (
	"io"
	"os"
)

// writeFile 将数据写入文件。
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

// saveUploadedFile 将上传的文件内容保存到指定路径。
// 用于处理 multipart/form-data 格式的文件上传。
func saveUploadedFile(path string, src io.Reader) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}
