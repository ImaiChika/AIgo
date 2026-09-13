package api

import (
	"encoding/json"
	"io"
	"net/http"
	"errors"
	"context"
	"aigo/internal/storage"
)

// readJSON 从 HTTP 请求体读取 JSON 并解析到目标结构体。
func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// writeJSON 写入 JSON 格式的 HTTP 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError 写入错误格式的 JSON 响应。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// withCORS 添加 CORS 跨域头。只对白名单内的来源放行：
// - 白名单为空：不设置跨域头（浏览器同源请求不受影响，Vite 开发走代理为同源）
// - 请求 Origin 在白名单内：回显该来源并放行
// - 其他来源：不带跨域头，浏览器将阻止响应
func withCORS(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			// 预检请求直接返回 204
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withJSON 设置默认 Content-Type 为 application/json。
func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

// errResponded 标记业务函数已自行写出错误响应（如 400 校验错误），
// 外层不再重复写响应，也不覆盖业务错误状态码。
var errResponded = errors.New("response already written")

// withAuditedTx 在单个数据库事务中执行业务写入与审计写入：
// 要么业务完成且审计留痕同时提交，要么整体回滚（审计一致性口径）。
// 存储不支持事务能力时退化为顺序执行（轻量测试存储路径）。
// business 可调用 writeError 后返回 errResponded 以保留业务错误状态码。
func (s *Server) withAuditedTx(w http.ResponseWriter, r *http.Request, op string,
	business func(txCtx context.Context) error, audit func(txCtx context.Context) error) bool {
	run := func(txCtx context.Context) error {
		if err := business(txCtx); err != nil {
			return err
		}
		return audit(txCtx)
	}
	var err error
	if txStore, ok := s.questionStore.(storage.TransactionStore); ok {
		err = txStore.WithTransaction(r.Context(), run)
	} else {
		err = run(r.Context())
	}
	if err == nil {
		return true
	}
	if errors.Is(err, errResponded) {
		return false
	}
	writeError(w, 500, op+"失败: "+err.Error())
	return false
}
