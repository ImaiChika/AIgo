package api

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"errors"
	"net/http"
)

func (s *Server) handleModelSampling(w http.ResponseWriter, r *http.Request) {
	store, ok := s.questionStore.(storage.ModelSamplingStore)
	if !ok {
		writeError(w, 503, "当前存储未启用调用参数配置")
		return
	}
	if r.Method == http.MethodPut {
		var req struct {
			GenerationTemperature *float64 `json:"generation_temperature"`
			CheckTemperature      *float64 `json:"check_temperature"`
			Revision              *int64   `json:"revision"`
		}
		if readJSON(r, &req) != nil || req.GenerationTemperature == nil || req.CheckTemperature == nil || req.Revision == nil || *req.Revision < 0 {
			writeError(w, 400, "请提供出题温度、首检温度及当前配置版本")
			return
		}
		c := domain.ModelSamplingConfig{GenerationTemperature: *req.GenerationTemperature, CheckTemperature: *req.CheckTemperature, Revision: *req.Revision}
		if err := c.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := store.SaveModelSamplingConfig(r.Context(), c); err != nil {
			if errors.Is(err, storage.ErrModelSamplingConflict) {
				writeError(w, 409, err.Error())
			} else {
				writeError(w, 500, "保存调用参数失败")
			}
			return
		}
		s.logAIProviderChange(r, "sampling_update", "出题与首检温度")
	}
	c, err := store.GetModelSamplingConfig(r.Context())
	if err != nil {
		writeError(w, 500, "读取调用参数失败")
		return
	}
	writeJSON(w, 200, c)
}
