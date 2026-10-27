package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/ffmpeg"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

type Server struct {
	store   *store.Store
	cctv    *cctv.Client
	manager *service.Manager
	log     *slog.Logger
}

func New(s *store.Store, client *cctv.Client, manager *service.Manager, logger *slog.Logger) http.Handler {
	server := &Server{store: s, cctv: client, manager: manager, log: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("GET /api/state", server.state)
	mux.HandleFunc("GET /api/search", server.search)
	mux.HandleFunc("GET /api/categories", server.categories)
	mux.HandleFunc("POST /api/categories", server.addCategory)
	mux.HandleFunc("PUT /api/categories/{id}", server.updateCategory)
	mux.HandleFunc("DELETE /api/categories/{id}", server.deleteCategory)
	mux.HandleFunc("POST /api/subscriptions", server.addSubscription)
	mux.HandleFunc("GET /api/subscriptions", server.subscriptions)
	mux.HandleFunc("PATCH /api/subscriptions/{id}", server.updateSubscription)
	mux.HandleFunc("DELETE /api/subscriptions/{id}", server.deleteSubscription)
	mux.HandleFunc("POST /api/subscriptions/{id}/check", server.checkSubscription)
	mux.HandleFunc("GET /api/downloads", server.downloads)
	mux.HandleFunc("POST /api/downloads/pause-all", server.pauseAllDownloads)
	mux.HandleFunc("POST /api/downloads/resume-all", server.resumeAllDownloads)
	mux.HandleFunc("POST /api/downloads/{id}/retry", server.retryDownload)
	mux.HandleFunc("POST /api/downloads/{id}/cancel", server.cancelDownload)
	mux.HandleFunc("GET /api/settings", server.settings)
	mux.HandleFunc("PUT /api/settings", server.updateSettings)
	return server.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !isAllowedOrigin(origin, r.Host) {
			if r.Method == http.MethodOptions {
				writeError(w, http.StatusForbidden, errors.New("不允许的请求来源"))
				return
			}
		} else if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

func isAllowedOrigin(origin, requestHost string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	requestHostname := requestHost
	if parsed, _, splitErr := net.SplitHostPort(requestHost); splitErr == nil {
		requestHostname = parsed
	}
	return host == requestHostname || host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	_, ffmpegErr := ffmpeg.Find()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	databaseOK := s.store.Ping(ctx) == nil
	ready := ffmpegErr == nil && databaseOK
	writeJSON(w, http.StatusOK, map[string]any{"ok": ready, "ffmpeg": ffmpegErr == nil, "playerDecryptor": true, "decryptorRuntime": "go-wasm", "database": databaseOK, "time": time.Now().Format(time.RFC3339)})
}

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	state := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":      state.Settings,
		"categories":    state.Categories,
		"subscriptions": state.Subscriptions,
		"downloads":     state.Downloads,
		"stats":         buildStats(state),
	})
}

func (s *Server) categories(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"categories": s.store.Snapshot().Categories})
}

type categoryInput struct {
	Name         string `json:"name"`
	ParentID     string `json:"parentId"`
	DownloadPath string `json:"downloadPath"`
}

func (s *Server) addCategory(w http.ResponseWriter, r *http.Request) {
	var input categoryInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	category, err := s.manager.AddCategory(input.Name, input.ParentID, input.DownloadPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, category)
}

func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	var input categoryInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	category, err := s.manager.UpdateCategory(r.PathValue("id"), input.Name, input.ParentID, input.DownloadPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, category)
}

func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.DeleteCategory(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	results, err := s.cctv.Search(ctx, r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) subscriptions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": s.store.Snapshot().Subscriptions})
}

func (s *Server) addSubscription(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SourceURL         string `json:"sourceUrl"`
		StorageCategoryID string `json:"storageCategoryId"`
		DownloadExisting  bool   `json:"downloadExisting"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	sub, err := s.manager.AddSubscription(ctx, input.SourceURL, input.StorageCategoryID, input.DownloadExisting)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status            string  `json:"status"`
		AutoDownload      *bool   `json:"autoDownload"`
		StorageCategoryID *string `json:"storageCategoryId"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.manager.UpdateSubscription(r.PathValue("id"), input.Status, input.AutoDownload, input.StorageCategoryID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.DeleteSubscription(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	for _, sub := range s.store.Snapshot().Subscriptions {
		if sub.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("订阅不存在"))
		return
	}
	s.manager.CheckSubscriptionAsync(id)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	all := s.store.Snapshot().Downloads
	if status == "" || status == "all" {
		writeJSON(w, http.StatusOK, map[string]any{"downloads": all})
		return
	}
	filtered := make([]model.DownloadTask, 0)
	for _, task := range all {
		if task.Status == status {
			filtered = append(filtered, task)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloads": filtered})
}

func (s *Server) retryDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.RetryTask(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.CancelTask(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) pauseAllDownloads(w http.ResponseWriter, _ *http.Request) {
	if err := s.manager.PauseAll(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "paused": true})
}

func (s *Server) resumeAllDownloads(w http.ResponseWriter, _ *http.Request) {
	if err := s.manager.ResumeAll(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "paused": false})
}

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().Settings)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input model.Settings
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.PollIntervalMinutes < 1 || input.PollIntervalMinutes > 10080 {
		writeError(w, http.StatusBadRequest, errors.New("轮询时间需在 1 到 10080 分钟之间"))
		return
	}
	if input.ConcurrentDownloads < 1 || input.ConcurrentDownloads > 10 {
		writeError(w, http.StatusBadRequest, errors.New("并发下载数需在 1 到 10 之间"))
		return
	}
	// Older Web clients do not send this field; retain their saved value.
	if input.SegmentConcurrency == 0 {
		input.SegmentConcurrency = s.store.Snapshot().Settings.SegmentConcurrency
	}
	if input.SegmentConcurrency < 1 || input.SegmentConcurrency > model.MaxSegmentConcurrency {
		writeError(w, http.StatusBadRequest, errors.New("单集分片并发数需在 1 到 16 之间"))
		return
	}
	input.DownloadPath = ""
	input.DownloadsPaused = s.store.Snapshot().Settings.DownloadsPaused
	if err := s.store.Update(func(state *model.State) error { state.Settings = input; return nil }); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.manager.Notify()
	writeJSON(w, http.StatusOK, input)
}

func buildStats(state model.State) map[string]int {
	stats := map[string]int{"subscriptions": len(state.Subscriptions), "queued": 0, "downloading": 0, "paused": 0, "completed": 0, "failed": 0}
	for _, task := range state.Downloads {
		if _, ok := stats[task.Status]; ok {
			stats[task.Status]++
		}
	}
	return stats
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求内容不正确: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error(), "status": strconv.Itoa(status)})
}
