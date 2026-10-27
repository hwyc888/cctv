package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

func completedVideoTestHandler(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	video := filepath.Join(dir, "completed.mp4")
	if err := os.WriteFile(video, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateStore, err := store.Open(context.Background(), filepath.Join(dir, "test.db"), dir, model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })
	if err := stateStore.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{
			{ID: "done", Status: "completed", FilePath: video},
			{ID: "waiting", Status: "downloading", FilePath: video},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	client := cctv.New()
	return New(stateStore, client, service.NewManager(stateStore, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCompletedVideoSupportsHTTPRange(t *testing.T) {
	handler := completedVideoTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/downloads/done/video", nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "2345" {
		t.Fatalf("range request: code=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 2-5/16" {
		t.Fatalf("wrong Content-Range %q", got)
	}
	if got := response.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("unexpected content-type %q", got)
	}
}

func TestVideoPlaybackRejectsUnfinishedOrUnknownTasks(t *testing.T) {
	handler := completedVideoTestHandler(t)
	for _, id := range []string{"waiting", "missing"} {
		request := httptest.NewRequest(http.MethodGet, "/api/downloads/"+id+"/video", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d body=%s", id, response.Code, response.Body.String())
		}
	}
}

func TestOpenFolderRejectsRemoteRequestsAndUnfinishedTasks(t *testing.T) {
	handler := completedVideoTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/downloads/done/open-directory", strings.NewReader("{}"))
	request.RemoteAddr = "203.0.113.7:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote open-directory request status %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/downloads/waiting/open-directory", strings.NewReader("{}"))
	request.RemoteAddr = "127.0.0.1:12345"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unfinished open-directory request status %d", response.Code)
	}
}
