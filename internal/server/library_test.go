package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

type libraryHTTPFixture func(*http.Request) (*http.Response, error)

func (f libraryHTTPFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLibraryBrowseToSelectedDownloadAPI(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "library.db"), filepath.Join(dir, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := cctv.New()
	guid := "0123456789abcdef0123456789abcdef"
	client.HTTPClient().Transport = libraryHTTPFixture(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/list/getVideoAlbumList":
			body = `{"data":{"list":[{"id":"VIDA123456789abc","title":"央视动画节目","image":"https://img.cctv.com/show.jpg"}]}}`
		case "/NewVideo/getVideoListByAlbumIdNew":
			body = `{"data":{"list":[{"guid":"`+guid+`","title":"央视动画 第1集","image":"https://img.cctv.com/episode.jpg"}]}}`
		default:
			t.Errorf("unexpected upstream route: %s", r.URL)
			body = "{}"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	handler := New(st, client, service.NewManager(st, client), slog.New(slog.NewTextHandler(io.Discard, nil)))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/library/programs?kind=animation&page=1", nil)
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("library program browse: %d %s", response.Code, response.Body.String())
	}
	var programs cctv.LibraryPrograms
	if err := json.Unmarshal(response.Body.Bytes(), &programs); err != nil || len(programs.Programs) != 1 {
		t.Fatalf("program list not available: %#v %v", programs, err)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/library/episodes?kind=animation&id=VIDA123456789abc&page=1", nil)
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("library episode browse: %d %s", response.Code, response.Body.String())
	}
	var episodes cctv.LibraryEpisodes
	if err := json.Unmarshal(response.Body.Bytes(), &episodes); err != nil || len(episodes.Episodes) != 1 || episodes.Episodes[0].VideoGUID != guid {
		t.Fatalf("episode list not available: %#v %v", episodes, err)
	}

	payload, _ := json.Marshal(map[string]any{
		"kind": "animation", "id": "VIDA123456789abc", "page": 1,
		"programTitle": "央视动画节目", "videoGuids": []string{guid},
		"storageCategoryId": st.Snapshot().Categories[0].ID,
	})
	for pass := 0; pass < 2; pass++ {
		response = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/api/library/downloads", bytes.NewReader(payload))
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("enqueue status %d: %s", response.Code, response.Body.String())
		}
		var stats map[string]int
		if err := json.Unmarshal(response.Body.Bytes(), &stats); err != nil || stats["added"] != 1-pass || stats["skipped"] != pass {
			t.Fatalf("unexpected enqueue stats %#v: %v", stats, err)
		}
	}
	if len(st.Snapshot().Downloads) != 1 || len(st.Snapshot().Subscriptions) != 0 {
		t.Fatal("library queue did not create exactly one direct task")
	}
}

func TestLibraryAPIRejectsInvalidPagesAndForeignOrigin(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "state.db"), filepath.Join(dir, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := cctv.New()
	handler := New(st, client, service.NewManager(st, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{
		"/api/library/programs?kind=animation&page=999",
		"/api/library/episodes?kind=animation&id="+url.QueryEscape("VIDA01234567")+"&page=-1",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid page accepted: %d %s", response.Code, path)
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/library/downloads", bytes.NewReader([]byte("{}")))
	request.Header.Set("Origin", "https://unrelated.example")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign origin accepted: %d", response.Code)
	}
}
