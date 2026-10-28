package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/store"
)

type queueMockTransport func(*http.Request) (*http.Response, error)

func (f queueMockTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

const (
	queueSource = "https://tv.cctv.com/album/index.shtml"
	queueFirst  = "https://tv.cctv.com/2026/06/29/VIDE1.shtml"
	queueSecond = "https://tv.cctv.com/2026/06/30/VIDE2.shtml"
)
const queueHTML = `<meta property="og:title" content="《测试节目》">
<script>var jsonData2 = [
{'title':'《测试节目》 第1集','img':'','brief':'one','url':'https://tv.cctv.com/2026/06/29/VIDE1.shtml'},
{'title':'《测试节目》 第2集','img':'','brief':'two','url':'https://tv.cctv.com/2026/06/30/VIDE2.shtml'}
];</script>`

func queueManagerForTest(t *testing.T) (*Manager, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(dir, "cctv.db"), filepath.Join(dir, "downloads"), model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })
	client := cctv.New()
	client.HTTPClient().Transport = queueMockTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != queueSource {
			return nil, errors.New("unexpected external URL")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(queueHTML)), Header: make(http.Header)}, nil
	})
	return NewManager(stateStore, client), stateStore, stateStore.Snapshot().Categories[0].ID
}

func TestQueueEpisodesOnlySelectedWithoutSubscription(t *testing.T) {
	manager, stateStore, categoryID := queueManagerForTest(t)
	added, skipped, err := manager.QueueEpisodes(context.Background(), queueSource, []string{queueSecond}, categoryID)
	if err != nil || added != 1 || skipped != 0 {
		t.Fatalf("first selected enqueue: added=%d skipped=%d err=%v", added, skipped, err)
	}
	state := stateStore.Snapshot()
	if len(state.Subscriptions) != 0 || len(state.Episodes) != 0 || len(state.Downloads) != 1 {
		t.Fatalf("direct download must not create a subscription: %#v", state)
	}
	task := state.Downloads[0]
	if task.PageURL != queueSecond || task.StorageCategoryID != categoryID || task.SubscriptionID != "" || task.Status != "queued" {
		t.Fatalf("invalid direct task: %#v", task)
	}
	added, skipped, err = manager.QueueEpisodes(context.Background(), queueSource, []string{queueSecond, queueFirst, queueFirst}, categoryID)
	if err != nil || added != 1 || skipped != 2 {
		t.Fatalf("dedupe failed: added=%d skipped=%d err=%v", added, skipped, err)
	}
	if len(stateStore.Snapshot().Downloads) != 2 {
		t.Fatal("duplicate task created")
	}
	if _, _, err := manager.QueueEpisodes(context.Background(), queueSource, []string{"https://example.com/a"}, categoryID); err == nil {
		t.Fatal("an arbitrary page URL must be rejected")
	}
	if len(stateStore.Snapshot().Downloads) != 2 {
		t.Fatal("invalid enqueue modified the queue")
	}
}

func TestQueueEpisodesRespectsPauseAndExistingSubscriptionTasks(t *testing.T) {
	manager, stateStore, _ := queueManagerForTest(t)
	category, err := manager.AddCategory("单次下载", "", filepath.Join(t.TempDir(), "videos"))
	if err != nil {
		t.Fatal(err)
	}
	categoryID := category.ID
	if err := stateStore.Update(func(state *model.State) error {
		state.Settings.DownloadsPaused = true
		state.Episodes = append(state.Episodes, model.Episode{ID: "sub_e1", PageURL: queueFirst, SubscriptionID: "sub"})
		state.Downloads = append(state.Downloads, model.DownloadTask{ID: "old", EpisodeID: "sub_e1", Status: "completed"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	added, skipped, err := manager.QueueEpisodes(context.Background(), queueSource, []string{queueFirst, queueSecond}, categoryID)
	if err != nil || added != 1 || skipped != 1 {
		t.Fatalf("cross-subscription dedupe failed: %d %d %v", added, skipped, err)
	}
	newTask := stateStore.Snapshot().Downloads[0]
	if newTask.PageURL != queueSecond || newTask.Status != "paused" {
		t.Fatalf("download was not safely paused: %#v", newTask)
	}
	if err := manager.DeleteCategory(categoryID); err == nil {
		t.Fatal("a category with direct downloads should not be removed")
	}
}
