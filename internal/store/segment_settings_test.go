package store

import (
	"context"
	"path/filepath"
	"testing"

	"cctv-sub/internal/model"
)

func TestSegmentConcurrencyPersistsAndUpgradesOldSettings(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "settings.db")
	defaults := model.Settings{PollIntervalMinutes: 30, ConcurrentDownloads: 2, SegmentConcurrency: model.DefaultSegmentConcurrency}
	s, err := Open(context.Background(), db, dir, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Settings.SegmentConcurrency; got != 4 {
		t.Fatalf("default segment concurrency = %d; want 4", got)
	}
	if err := s.Update(func(state *model.State) error {
		state.Settings.SegmentConcurrency = 8
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), db, dir, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().Settings.SegmentConcurrency; got != 8 {
		t.Fatalf("saved segment concurrency = %d; want 8", got)
	}
	if err := reopened.Update(func(state *model.State) error {
		state.Settings.SegmentConcurrency = 0 // Earlier versions did not save this field.
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(context.Background(), db, dir, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if got := upgraded.Snapshot().Settings.SegmentConcurrency; got != model.DefaultSegmentConcurrency {
		t.Fatalf("upgraded segment concurrency = %d; want %d", got, model.DefaultSegmentConcurrency)
	}
}
