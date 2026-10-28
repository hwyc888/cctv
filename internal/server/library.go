package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func libraryPageNumber(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 || page > 200 {
		return 0, errors.New("页码须在 1 到 200 之间")
	}
	return page, nil
}

func (s *Server) libraryPrograms(w http.ResponseWriter, r *http.Request) {
	page, err := libraryPageNumber(r.URL.Query().Get("page"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	items, err := s.cctv.BrowseLibrary(ctx, r.URL.Query().Get("kind"), r.URL.Query().Get("channel"), page)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) libraryEpisodes(w http.ResponseWriter, r *http.Request) {
	page, err := libraryPageNumber(r.URL.Query().Get("page"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := s.cctv.BrowseLibraryEpisodes(ctx, r.URL.Query().Get("kind"), r.URL.Query().Get("id"), page)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) addLibraryDownloads(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind              string   `json:"kind"`
		ID                string   `json:"id"`
		Page              int      `json:"page"`
		ProgramTitle      string   `json:"programTitle"`
		VideoGUIDs        []string `json:"videoGuids"`
		StorageCategoryID string   `json:"storageCategoryId"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Page < 1 || input.Page > 200 || len(input.VideoGUIDs) == 0 || len(input.VideoGUIDs) > 100 {
		writeError(w, http.StatusBadRequest, errors.New("请选择当前页的有效视频（最多 100 个）"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	added, skipped, err := s.manager.QueueLibraryEpisodes(ctx, strings.TrimSpace(input.Kind), input.ID, input.ProgramTitle, input.Page, input.VideoGUIDs, input.StorageCategoryID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"added": added, "skipped": skipped})
}
