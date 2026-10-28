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

type directAPITransport func(*http.Request) (*http.Response, error)

func (f directAPITransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDirectLinkPreviewAndSelectedDownloadAPI(t *testing.T) {
	directory := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(directory, "database.db"), filepath.Join(directory, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	client := cctv.New()
	client.HTTPClient().Transport = directAPITransport(func(req *http.Request) (*http.Response, error) {
		source := `<meta property="og:title" content="《测试栏目》"><script>var jsonData2=[{'title':'《测试栏目》 第1集','img':'','brief':'one','url':'https://tv.cctv.com/2026/06/29/VIDE1.shtml'},{'title':'《测试栏目》 第2集','img':'','brief':'two','url':'https://tv.cctv.com/2026/06/30/VIDE2.shtml'}]</script>`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(source))}, nil
	})
	handler := New(stateStore, client, service.NewManager(stateStore, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
	sourceURL := "https://tv.cctv.com/album/index.shtml"
	request := httptest.NewRequest(http.MethodGet, "/api/resolve?url="+url.QueryEscape(sourceURL), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview failed: %d: %s", response.Code, response.Body.String())
	}
	var series model.Series
	if err := json.Unmarshal(response.Body.Bytes(), &series); err != nil {
		t.Fatal(err)
	}
	if series.EpisodeCount != 2 || len(series.Episodes) != 2 {
		t.Fatalf("preview did not return selectable episodes: %#v", series)
	}
	categoryID := stateStore.Snapshot().Categories[0].ID
	payload := map[string]any{
		"sourceUrl": sourceURL,
		"episodeUrls": []string{series.Episodes[1].PageURL},
		"storageCategoryId": categoryID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		request = httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader(body))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("enqueue %d failed: %d %s", index, response.Code, response.Body.String())
		}
		var result map[string]int
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["added"] != 1-index || result["skipped"] != index {
			t.Fatalf("queue response: %#v", result)
		}
	}
	state := stateStore.Snapshot()
	if len(state.Downloads) != 1 || len(state.Subscriptions) != 0 {
		t.Fatalf("direct downloads unexpectedly created subscriptions: %#v", state)
	}
}

func TestDirectDownloadRejectsUnrecognizedEpisodeAndForeignOrigin(t *testing.T) {
	directory := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(directory, "database.db"), filepath.Join(directory, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	client := cctv.New()
	client.HTTPClient().Transport = directAPITransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<meta property="og:title" content="《测试》"><script>var jsonData2=[{'title':'第1集','img':'','brief':'','url':'https://tv.cctv.com/2026/06/30/VIDE1.shtml'}]</script>`))}, nil
	})
	handler := New(stateStore, client, service.NewManager(stateStore, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
	body, _ := json.Marshal(map[string]any{
		"sourceUrl": "https://tv.cctv.com/album/index.shtml",
		"episodeUrls": []string{"https://other.example/VIDE1.shtml"},
		"storageCategoryId": stateStore.Snapshot().Categories[0].ID,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unrecognized URL accepted: status=%d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader(body))
	request.Header.Set("Origin", "https://malicious.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign origin accepted: status=%d %s", response.Code, response.Body.String())
	}
}
