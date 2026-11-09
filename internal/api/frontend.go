package api

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// WithStaticFrontend 在同一 HTTP 进程中托管 Vite 生产构建产物。
// API、健康检查和已移除的旧资源前缀始终交给 backend；其他 GET/HEAD 请求按静态
// 文件处理，不存在的前端路由回退到 index.html，支持 Vue history 刷新。
func WithStaticFrontend(backend http.Handler, distDir string) (http.Handler, error) {
	if backend == nil {
		return nil, fmt.Errorf("后端 Handler 不能为空")
	}
	absDir, err := filepath.Abs(strings.TrimSpace(distDir))
	if err != nil {
		return nil, fmt.Errorf("解析前端静态目录失败: %w", err)
	}
	indexPath := filepath.Join(absDir, "index.html")
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("前端静态目录不可用（缺少 index.html）: %w", err)
	}
	if !indexInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("前端入口不是普通文件: %s", indexPath)
	}
	indexContents, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("读取前端入口失败: %w", err)
	}

	root := os.DirFS(absDir)
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBackendPath(r.URL.Path) {
			backend.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		name, valid := frontendFileName(r.URL.Path)
		if !valid {
			http.NotFound(w, r)
			return
		}
		if name == "index.html" {
			name = ""
		}
		if name != "" {
			if info, statErr := fs.Stat(root, name); statErr == nil && info.Mode().IsRegular() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				w.Header().Set("X-Content-Type-Options", "nosniff")
				files.ServeHTTP(w, r)
				return
			}
		}

		// 文件型请求缺失时必须返回 404，不能用 HTML 冒充 JS/CSS/图片。
		if name == "assets" || strings.HasPrefix(name, "assets/") || (name != "" && path.Ext(name) != "") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "index.html", indexInfo.ModTime(), bytes.NewReader(indexContents))
	}), nil
}

func isBackendPath(requestPath string) bool {
	for _, prefix := range []string{"/api", "/health", "/images"} {
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}

func frontendFileName(requestPath string) (string, bool) {
	cleaned := path.Clean("/" + requestPath)
	name := strings.TrimPrefix(cleaned, "/")
	if name == "" || name == "." {
		return "", true
	}
	if !fs.ValidPath(name) {
		return "", false
	}
	if strings.Contains(name, "\\") {
		return "", false
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	return name, true
}
