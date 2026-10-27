package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/store"
)

func TestReplaceDownloadedFileSafelyOverwritesOldVideo(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "episode.mp4")
	temporary := filepath.Join(dir, "episode.mp4.part")
	if err := os.WriteFile(final, []byte("old corrupted video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new verified video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceDownloadedFile(temporary, final); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(final)
	if err != nil || string(content) != "new verified video" {
		t.Fatalf("file replacement failed: %q %v", content, err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary file was not moved: %v", err)
	}
	matches, err := filepath.Glob(final + ".backup*")
	if err != nil || len(matches) != 0 {
		t.Fatalf("unexpected leftover backups: %v %v", matches, err)
	}
}

func TestRetryCompletedDownloadKeepsOldFileUntilNewOneIsReady(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "episode.mp4")
	if err := os.WriteFile(original, []byte("previous video"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateStore, err := store.Open(context.Background(), filepath.Join(dir, "state.db"), dir, model.Settings{
		PollIntervalMinutes: 30, ConcurrentDownloads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	if err := stateStore.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{{ID: "done", Status: "completed", FilePath: original, VideoQuality: "720×480", Progress: 100}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(stateStore, cctv.New())
	if err := manager.RetryTask("done"); err != nil {
		t.Fatal(err)
	}
	task := stateStore.Snapshot().Downloads[0]
	if task.Status != "queued" || task.Progress != 0 || task.VideoQuality != "" || task.FilePath != original {
		t.Fatalf("completed task was not requeued safely: %#v", task)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("old file disappeared before verified replacement: %v", err)
	}
}
