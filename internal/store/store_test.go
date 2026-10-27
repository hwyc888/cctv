package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cctv-sub/internal/model"
)

func TestOpenMigratesLegacyJSONAndPersistsSQLite(t *testing.T) {
	dataDir := t.TempDir()
	legacy := model.State{
		Settings: model.Settings{
			DownloadPath:             "/legacy/downloads",
			PollIntervalMinutes:      12,
			ConcurrentDownloads:      3,
			VerifyVideoAfterDownload: true,
			DownloadsPaused:          true,
		},
		Subscriptions: []model.Subscription{{ID: "sub-1", Title: "测试剧"}},
		Downloads:     []model.DownloadTask{{ID: "task-1", Status: "downloading", Speed: "1 MB/s"}},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dataDir, "cctv-sub.db")
	defaults := model.Settings{DownloadPath: "/default", PollIntervalMinutes: 30, ConcurrentDownloads: 2}

	stateStore, err := Open(context.Background(), databasePath, dataDir, defaults)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := stateStore.Snapshot()
	if !snapshot.Settings.DownloadsPaused || !snapshot.Settings.VerifyVideoAfterDownload || snapshot.Settings.DownloadPath != "" {
		t.Fatalf("legacy settings were not migrated: %#v", snapshot.Settings)
	}
	if len(snapshot.Categories) != 1 || snapshot.Categories[0].DownloadPath != "/legacy/downloads" {
		t.Fatalf("legacy download path was not migrated to a default category: %#v", snapshot.Categories)
	}
	if len(snapshot.Subscriptions) != 1 || snapshot.Subscriptions[0].Title != "测试剧" {
		t.Fatalf("legacy subscriptions were not migrated: %#v", snapshot.Subscriptions)
	}
	if snapshot.Subscriptions[0].StorageCategoryID != snapshot.Categories[0].ID {
		t.Fatalf("legacy subscription was not assigned to the default category: %#v", snapshot.Subscriptions[0])
	}
	if len(snapshot.Downloads) != 1 || snapshot.Downloads[0].Status != "paused" || snapshot.Downloads[0].Speed != "" {
		t.Fatalf("in-flight task was not safely paused during migration: %#v", snapshot.Downloads)
	}
	if err := stateStore.Update(func(state *model.State) error {
		state.Settings.PollIntervalMinutes = 45
		state.Settings.VerifyVideoAfterDownload = false
		state.Subscriptions[0].Description = "SQLite persistence"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), databasePath, dataDir, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded := reopened.Snapshot()
	if reloaded.Settings.PollIntervalMinutes != 45 || reloaded.Settings.VerifyVideoAfterDownload || reloaded.Subscriptions[0].Description != "SQLite persistence" {
		t.Fatalf("SQLite state did not survive reopen: %#v", reloaded)
	}
}
