package service

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/store"
)

func TestQueueSelectedSpecialProgramVideosByGUID(t *testing.T) {
	directory := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(directory, "state.db"), filepath.Join(directory, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const pageURL = "https://tv.cctv.com/2025/05/29/VIDAofwleevTNcsQH3PjpAy0250529.shtml"
	const guid = "0123456789abcdef0123456789abcdef"
	const episodeURL = "https://tv.cctv.com/2025/05/29/VIDEfirst.shtml"
	client := cctv.New()
	client.HTTPClient().Transport = queueMockTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/2025/05/29/VIDAofwleevTNcsQH3PjpAy0250529.shtml":
			body = `<meta property="og:title" content="CMG世界机器人大赛 系列赛">`
		case "/NewVideo/getVideoListByAlbumIdNew":
			if req.URL.Query().Get("mode") != "2" {
				t.Errorf("wrong upstream mode %s", req.URL)
			}
			body = `{"data":{"list":[{"guid":"`+guid+`","title":"机甲格斗赛","url":"`+episodeURL+`"}]}}`
		default:
			t.Errorf("unexpected network request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	manager := NewManager(st, client)
	category := st.Snapshot().Categories[0].ID
	added, skipped, err := manager.QueueEpisodes(context.Background(), pageURL, []string{guid, guid}, category)
	if err != nil || added != 1 || skipped != 1 {
		t.Fatalf("selecting video GUID failed: added=%d skipped=%d err=%v", added, skipped, err)
	}
	downloads := st.Snapshot().Downloads
	if len(downloads) != 1 || downloads[0].VideoGUID != guid || downloads[0].PageURL != episodeURL || downloads[0].SubscriptionID != "" {
		t.Fatalf("wrong queued task: %#v", downloads)
	}
	added, skipped, err = manager.QueueEpisodes(context.Background(), pageURL, []string{episodeURL}, category)
	if err != nil || added != 0 || skipped != 1 {
		t.Fatalf("duplicate URL and GUID were not deduplicated: %d %d %v", added, skipped, err)
	}
	if _, _, err := manager.QueueEpisodes(context.Background(), pageURL, []string{"ffffffffffffffffffffffffffffffff"}, category); err == nil {
		t.Fatal("accepted a GUID not present in the official program")
	}
}

func TestSubscribedSpecialProgramRetainsGUID(t *testing.T) {
	episode := model.Episode{Title: "机器人节目", PageURL: "https://tv.cctv.com/2025/08/07/VIDEexample.shtml", VideoGUID: "0123456789abcdef0123456789abcdef"}
	task := newTask(model.Subscription{ID: "subscription-1", Title: "机器人赛事"}, episode, "now")
	if task.VideoGUID != episode.VideoGUID || task.PageURL != episode.PageURL {
		t.Fatalf("subscription task lost the resolved video GUID: %#v", task)
	}
}
