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

type libraryTestTransport func(*http.Request) (*http.Response, error)

func (f libraryTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const libraryTestGUID = "0123456789abcdef0123456789abcdef"
const libraryTestGUID2 = "fedcba9876543210fedcba9876543210"

func newLibraryTestManager(t *testing.T) (*Manager, *store.Store, string) {
	t.Helper()
	directory := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(directory, "state.db"), filepath.Join(directory, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	client := cctv.New()
	client.HTTPClient().Transport = libraryTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/NewVideo/getVideoListByAlbumIdNew" || r.URL.Query().Get("id") != "VIDA123456789abc" {
			t.Errorf("unexpected library API request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":{"list":[{"guid":"`+libraryTestGUID+`","title":"央视动画 第1集","image":"https://img.cctv.com/1.jpg"},{"guid":"`+libraryTestGUID2+`","title":"央视动画 第2集"}]}}`))}, nil
	})
	return NewManager(st, client), st, st.Snapshot().Categories[0].ID
}

func TestQueueLibraryVideosWithoutSubscription(t *testing.T) {
	manager, st, categoryID := newLibraryTestManager(t)
	added, skipped, err := manager.QueueLibraryEpisodes(context.Background(), "animation", "VIDA123456789abc", "央视动画", 1, []string{libraryTestGUID}, categoryID)
	if err != nil || added != 1 || skipped != 0 {
		t.Fatalf("enqueue library video: %d %d %v", added, skipped, err)
	}
	snapshot := st.Snapshot()
	if len(snapshot.Subscriptions) != 0 || len(snapshot.Downloads) != 1 || snapshot.Downloads[0].VideoGUID != libraryTestGUID || snapshot.Downloads[0].SeriesTitle != "央视动画" {
		t.Fatalf("did not create ad-hoc GUID task: %#v", snapshot)
	}
	added, skipped, err = manager.QueueLibraryEpisodes(context.Background(), "animation", "VIDA123456789abc", "央视动画", 1, []string{libraryTestGUID, libraryTestGUID2, libraryTestGUID2}, categoryID)
	if err != nil || added != 1 || skipped != 2 {
		t.Fatalf("duplicate GUIDs not filtered: %d %d %v", added, skipped, err)
	}
	if len(st.Snapshot().Downloads) != 2 {
		t.Fatal("created duplicate tasks")
	}
	if _, _, err := manager.QueueLibraryEpisodes(context.Background(), "animation", "VIDA123456789abc", "央视动画", 1, []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, categoryID); err == nil {
		t.Fatal("unlisted GUID was accepted")
	}
	if len(st.Snapshot().Downloads) != 2 {
		t.Fatal("invalid selection changed task queue")
	}
}

func TestQueueLibraryRespectsGlobalPause(t *testing.T) {
	manager, st, categoryID := newLibraryTestManager(t)
	if err := st.Update(func(state *model.State) error {
		state.Settings.DownloadsPaused = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	added, _, err := manager.QueueLibraryEpisodes(context.Background(), "animation", "VIDA123456789abc", "央视动画", 1, []string{libraryTestGUID}, categoryID)
	if err != nil || added != 1 || st.Snapshot().Downloads[0].Status != "paused" {
		t.Fatalf("global pause not honored: added=%d err=%v state=%#v", added, err, st.Snapshot().Downloads)
	}
}
