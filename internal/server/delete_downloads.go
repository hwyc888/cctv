package server

import (
	"net/http"

	"cctv-sub/internal/service"
)

func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request) {
	result, err := s.manager.DeleteTasks([]string{r.PathValue("id")})
	writeDeleteDownloadResult(w, result, err)
}

func (s *Server) deleteDownloadsBatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.manager.DeleteTasks(input.IDs)
	writeDeleteDownloadResult(w, result, err)
}

func writeDeleteDownloadResult(w http.ResponseWriter, result service.DeleteTasksResult, err error) {
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
