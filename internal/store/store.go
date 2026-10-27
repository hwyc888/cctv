package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cctv-sub/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	mu    sync.RWMutex
	db    *sql.DB
	state model.State
}

func Open(ctx context.Context, databasePath, dataDir string, defaults model.Settings) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if databasePath == "" {
		databasePath = filepath.Join(dataDir, "cctv-sub.db")
	}
	absPath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("create SQLite directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(absPath) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	openCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := db.PingContext(openCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}
	if _, err := db.ExecContext(openCtx, `CREATE TABLE IF NOT EXISTS app_state (
		id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
		payload TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create SQLite state table: %w", err)
	}
	_, _ = db.ExecContext(openCtx, "PRAGMA optimize")

	s := &Store{db: db}
	var payload []byte
	err = db.QueryRowContext(openCtx, "SELECT payload FROM app_state WHERE id = 1").Scan(&payload)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = db.Close()
		return nil, fmt.Errorf("read SQLite state: %w", err)
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &s.state); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("decode SQLite state: %w", err)
		}
	} else {
		legacyPath := filepath.Join(dataDir, "state.json")
		legacy, readErr := os.ReadFile(legacyPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			_ = db.Close()
			return nil, fmt.Errorf("read legacy state: %w", readErr)
		}
		if len(legacy) > 0 {
			if err := json.Unmarshal(legacy, &s.state); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("decode legacy state: %w", err)
			}
		}
	}
	normalize(&s.state, defaults)
	if err := s.saveLocked(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Snapshot() model.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.state)
}

func (s *Store) Update(fn func(*model.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := clone(s.state)
	if err := fn(&s.state); err != nil {
		s.state = before
		return err
	}
	if err := s.saveLocked(); err != nil {
		s.state = before
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	payload, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_state (id, payload, updated_at) VALUES (1, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET payload = excluded.payload, updated_at = CURRENT_TIMESTAMP`, string(payload))
	if err != nil {
		return fmt.Errorf("save SQLite state: %w", err)
	}
	return nil
}

func normalize(state *model.State, defaults model.Settings) {
	legacyDownloadPath := state.Settings.DownloadPath
	if legacyDownloadPath == "" {
		legacyDownloadPath = defaults.DownloadPath
	}
	if state.Settings.PollIntervalMinutes < 1 {
		state.Settings.PollIntervalMinutes = defaults.PollIntervalMinutes
	}
	if state.Settings.ConcurrentDownloads < 1 {
		state.Settings.ConcurrentDownloads = defaults.ConcurrentDownloads
	}
	if state.Settings.SegmentConcurrency < 1 || state.Settings.SegmentConcurrency > model.MaxSegmentConcurrency {
		state.Settings.SegmentConcurrency = defaults.SegmentConcurrency
		if state.Settings.SegmentConcurrency < 1 || state.Settings.SegmentConcurrency > model.MaxSegmentConcurrency {
			state.Settings.SegmentConcurrency = model.DefaultSegmentConcurrency
		}
	}
	if state.Categories == nil {
		state.Categories = []model.StorageCategory{}
	}
	if len(state.Categories) == 0 {
		if legacyDownloadPath == "" {
			legacyDownloadPath = "downloads"
		}
		state.Categories = append(state.Categories, model.StorageCategory{
			ID: "category_default", Name: "默认分类", DownloadPath: legacyDownloadPath, CreatedAt: time.Now().Format(time.RFC3339),
		})
	}
	defaultCategoryID := state.Categories[0].ID
	for i := range state.Categories {
		if state.Categories[i].DownloadPath == "" {
			state.Categories[i].DownloadPath = legacyDownloadPath
		}
	}
	state.Settings.DownloadPath = ""
	if state.Subscriptions == nil {
		state.Subscriptions = []model.Subscription{}
	}
	for i := range state.Subscriptions {
		if state.Subscriptions[i].StorageCategoryID == "" {
			state.Subscriptions[i].StorageCategoryID = defaultCategoryID
		}
	}
	if state.Episodes == nil {
		state.Episodes = []model.Episode{}
	}
	if state.Downloads == nil {
		state.Downloads = []model.DownloadTask{}
	}
	for i := range state.Downloads {
		if state.Downloads[i].Status != "downloading" {
			continue
		}
		if state.Settings.DownloadsPaused {
			state.Downloads[i].Status = "paused"
		} else {
			state.Downloads[i].Status = "queued"
		}
		state.Downloads[i].Speed = ""
	}
}

func clone(in model.State) model.State {
	out := in
	out.Categories = append([]model.StorageCategory{}, in.Categories...)
	out.Subscriptions = append([]model.Subscription{}, in.Subscriptions...)
	out.Episodes = append([]model.Episode{}, in.Episodes...)
	out.Downloads = append([]model.DownloadTask{}, in.Downloads...)
	return out
}
