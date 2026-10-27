package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

func TestIsAllowedOriginAcceptsGatewayHost(t *testing.T) {
	if !isAllowedOrigin("http://10.0.0.2:3010", "10.0.0.2") {
		t.Fatal("same-host NAS gateway origin should be allowed")
	}
	if !isAllowedOrigin("http://localhost:3000", "127.0.0.1:8080") {
		t.Fatal("local development origin should be allowed")
	}
	if isAllowedOrigin("https://example.com", "10.0.0.2") {
		t.Fatal("unrelated origin should be rejected")
	}
}

func TestUpdateSettingsPersistsVideoVerificationSwitch(t *testing.T) {
	dataDir := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(dataDir, "state.db"), dataDir, model.Settings{
		DownloadPath:        filepath.Join(dataDir, "downloads"),
		PollIntervalMinutes: 30,
		ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	if err := stateStore.Update(func(state *model.State) error {
		state.Settings.DownloadsPaused = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	manager := service.NewManager(stateStore, cctv.New())
	handler := New(stateStore, cctv.New(), manager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload, err := json.Marshal(model.Settings{
		PollIntervalMinutes:      45,
		ConcurrentDownloads:      3,
		VerifyVideoAfterDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(payload))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings update failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	settings := stateStore.Snapshot().Settings
	if !settings.VerifyVideoAfterDownload || !settings.DownloadsPaused || settings.SegmentConcurrency != model.DefaultSegmentConcurrency {
		t.Fatalf("verification switch, pause state, or legacy segment setting was not persisted: %#v", settings)
	}
}

func TestUpdateSettingsRejectsInvalidSegmentConcurrency(t *testing.T) {
	dataDir := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(dataDir, "state.db"), dataDir, model.Settings{
		DownloadPath: filepath.Join(dataDir, "downloads"),
		PollIntervalMinutes: 30,
		ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	client := cctv.New()
	handler := New(stateStore, client, service.NewManager(stateStore, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, value := range []int{-1, model.MaxSegmentConcurrency + 1} {
		payload, err := json.Marshal(model.Settings{PollIntervalMinutes: 30, ConcurrentDownloads: 2, SegmentConcurrency: value})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(payload))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("segment concurrency %d: expected 400, got %d: %s", value, response.Code, response.Body.String())
		}
	}
}

func TestCategoryAPIManagesNestedSavePaths(t *testing.T) {
	dataDir := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(dataDir, "state.db"), dataDir, model.Settings{
		DownloadPath:        filepath.Join(dataDir, "downloads"),
		PollIntervalMinutes: 30,
		ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	client := cctv.New()
	handler := New(stateStore, client, service.NewManager(stateStore, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
	defaultCategory := stateStore.Snapshot().Categories[0]
	payload, err := json.Marshal(categoryInput{Name: "动画", ParentID: defaultCategory.ID, DownloadPath: filepath.Join(dataDir, "animation")})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/categories", bytes.NewReader(payload))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create category failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var category model.StorageCategory
	if err := json.Unmarshal(recorder.Body.Bytes(), &category); err != nil {
		t.Fatal(err)
	}
	if category.ParentID != defaultCategory.ID || category.DownloadPath != filepath.Join(dataDir, "animation") {
		t.Fatalf("nested category was not persisted: %#v", category)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/categories/"+category.ID, nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete category failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
