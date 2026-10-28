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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

// This reproduces the user's exact flow from the browser's HTTP API to the
// official CCTV video list, then into the download queue (without downloading).
func TestLiveSpecialHTTPSelection(t *testing.T) {
	if os.Getenv("CCTV_LIBRARY_INTEGRATION") != "1" {
		t.Skip("enable CCTV_LIBRARY_INTEGRATION to test live CCTV metadata")
	}
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
	for _, test := range []struct {
		pageURL string
		want int
	}{
		{"https://tv.cctv.com/2025/05/29/VIDAofwleevTNcsQH3PjpAy0250529.shtml", 16},
		{"https://tv.cctv.com/2025/08/06/VIDACeLVEwlatefxEv4B3A4Z250806.shtml", 1},
	} {
		get := httptest.NewRequest(http.MethodGet, "/api/resolve?url="+url.QueryEscape(test.pageURL), nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, get)
		if response.Code != http.StatusOK {
			t.Fatalf("HTTP resolve %s returned %d: %s", test.pageURL, response.Code, response.Body.String())
		}
		var series model.Series
		if err := json.Unmarshal(response.Body.Bytes(), &series); err != nil {
			t.Fatal(err)
		}
		t.Logf("HTTP resolve title=%s episodes=%d expected=%d", series.Title, len(series.Episodes), test.want)
		if len(series.Episodes) != test.want || series.EpisodeCount != test.want {
			t.Fatalf("HTTP resolve selected %d instead of %d: %#v", len(series.Episodes), test.want, series.SearchResult)
		}
		episode := series.Episodes[0]
		if episode.VideoGUID == "" {
			t.Fatalf("resolved episode does not have a CCTV video GUID: %#v", episode)
		}
		requestBody, _ := json.Marshal(map[string]any{
			"sourceUrl": test.pageURL, "episodeUrls": []string{episode.VideoGUID},
			"storageCategoryId": st.Snapshot().Categories[0].ID,
		})
		for requestIndex := 0; requestIndex < 2; requestIndex++ {
			request := httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader(requestBody))
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusCreated {
				t.Fatalf("selected video HTTP POST failed (%d): %s", result.Code, result.Body.String())
			}
			var counts map[string]int
			if err := json.Unmarshal(result.Body.Bytes(), &counts); err != nil {
				t.Fatal(err)
			}
			if counts["added"] != 1-requestIndex || counts["skipped"] != requestIndex {
				t.Fatalf("unexpected deduplication result: %#v", counts)
			}
		}
	}
	if len(st.Snapshot().Downloads) != 2 || len(st.Snapshot().Subscriptions) != 0 {
		t.Fatalf("selection should add exactly two direct downloads: %#v", st.Snapshot().Downloads)
	}
}

func TestHealthIncludesBuildRevision(t *testing.T) {
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
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	body, err := io.ReadAll(response.Body)
	if err != nil { t.Fatal(err) }
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil { t.Fatal(err) }
	if payload["revision"] != BuildRevision {
		t.Fatalf("health version is absent: %s", body)
	}
}

func TestLiveAnimationHTTPSelection(t *testing.T) {
	if os.Getenv("CCTV_LIBRARY_INTEGRATION") != "1" {
		t.Skip("enable real CCTV API tests")
	}
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
	const albumID = "VIDATdlHRnTKXUY171juqvUA261010"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/library/episodes?kind=animation&id="+albumID+"&page=1", nil))
	if response.Code != 200 {
		t.Fatalf("HTTP animation episode lookup failed: %d %s", response.Code, response.Body.String())
	}
	var data cctv.LibraryEpisodes
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Total != 26 || data.Page != 1 || len(data.Episodes) != 26 {
		t.Fatalf("the actual Tom Cat album should offer 26 episodes, got total=%d page=%d length=%d", data.Total, data.Page, len(data.Episodes))
	}
	t.Logf("HTTP animation catalog: total=%d, selectable=%d, first=%s", data.Total, len(data.Episodes), data.Episodes[0].Title)
	payload, _ := json.Marshal(map[string]any{
		"kind": "animation", "id": albumID, "page": data.Page,
		"programTitle": "汤姆猫超能战队（第一季）",
		"videoGuids": []string{data.Episodes[0].VideoGUID},
		"storageCategoryId": st.Snapshot().Categories[0].ID,
	})
	for n := 0; n < 2; n++ {
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/api/library/downloads", bytes.NewReader(payload)))
		if result.Code != 201 {
			t.Fatalf("enqueue animation episode failed %d: %s", result.Code, result.Body.String())
		}
		var stats map[string]int
		if err := json.Unmarshal(result.Body.Bytes(), &stats); err != nil {
			t.Fatal(err)
		}
		if stats["added"] != 1-n || stats["skipped"] != n {
			t.Fatalf("duplicate handling failed: %#v", stats)
		}
	}
	// Real pagination regression: this cartoon contains more than 100 videos.
	programsResponse := httptest.NewRecorder()
	handler.ServeHTTP(programsResponse, httptest.NewRequest(http.MethodGet, "/api/library/programs?kind=animation&page=1", nil))
	var catalog cctv.LibraryPrograms
	if programsResponse.Code != 200 || json.Unmarshal(programsResponse.Body.Bytes(), &catalog) != nil {
		t.Fatalf("cannot retrieve real animation program page: %s", programsResponse.Body.String())
	}
	var rescueID string
	for _, program := range catalog.Programs {
		if strings.Contains(program.Title, "丛林救援队") {
			rescueID = program.ID
			break
		}
	}
	if rescueID == "" {
		t.Fatal("second-page regression series was not found")
	}
	nextResponse := httptest.NewRecorder()
	handler.ServeHTTP(nextResponse, httptest.NewRequest(http.MethodGet, "/api/library/episodes?kind=animation&id="+rescueID+"&page=2", nil))
	var next cctv.LibraryEpisodes
	if nextResponse.Code != 200 || json.Unmarshal(nextResponse.Body.Bytes(), &next) != nil ||
		next.Page != 2 || next.Total <= 100 || len(next.Episodes) == 0 || next.Episodes[0].Number != 101 {
		t.Fatalf("second page did not return playable episodes: %d %s", nextResponse.Code, nextResponse.Body.String())
	}
	secondPayload, _ := json.Marshal(map[string]any{
		"kind": "animation", "id": rescueID, "page": 2,
		"programTitle": "丛林救援队 火速到达", "videoGuids": []string{next.Episodes[0].VideoGUID},
		"storageCategoryId": st.Snapshot().Categories[0].ID,
	})
	secondResult := httptest.NewRecorder()
	handler.ServeHTTP(secondResult, httptest.NewRequest(http.MethodPost, "/api/library/downloads", bytes.NewReader(secondPayload)))
	if secondResult.Code != 201 {
		t.Fatalf("cannot download a selected episode from page 2: %d %s", secondResult.Code, secondResult.Body.String())
	}
	t.Logf("HTTP second page verified: total=%d videos-on-page=%d", next.Total, len(next.Episodes))
	if len(st.Snapshot().Downloads) != 2 || len(st.Snapshot().Subscriptions) != 0 {
		t.Fatal("selection should create two ad hoc tasks, including one from the second page")
	}
}
