package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/cctvwasm"
	"cctv-sub/internal/ffmpeg"
	"cctv-sub/internal/model"
	"cctv-sub/internal/store"
)

type Manager struct {
	store *store.Store
	cctv  *cctv.Client
	wake  chan struct{}

	mu       sync.Mutex
	active   map[string]context.CancelFunc
	checking map[string]bool

	workerMu       sync.Mutex
	workerSource   []byte
	workerLoadedAt time.Time
	engineMu       sync.Mutex
	engine         *cctvwasm.Engine
}

const (
	cctvWorkerURL      = "https://player.cntv.cn/h5vod/cctv.worker.js"
	cctvWorkerCacheAge = 6 * time.Hour
	cctvPlayerOrigin   = "http://tv.cctv.com:3010"
)

func NewManager(s *store.Store, client *cctv.Client) *Manager {
	return &Manager{
		store: s, cctv: client, wake: make(chan struct{}, 1),
		active: map[string]context.CancelFunc{}, checking: map[string]bool{},
	}
}

func (m *Manager) Start(ctx context.Context) {
	go m.dispatchLoop(ctx)
	go m.pollLoop(ctx)
	m.Notify()
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) AddCategory(name, parentID, downloadPath string) (model.StorageCategory, error) {
	name, parentID = strings.TrimSpace(name), strings.TrimSpace(parentID)
	downloadPath, err := normalizeCategoryPath(downloadPath)
	if err != nil {
		return model.StorageCategory{}, err
	}
	if name == "" {
		return model.StorageCategory{}, errors.New("分类名称不能为空")
	}
	if err := os.MkdirAll(downloadPath, 0o755); err != nil {
		return model.StorageCategory{}, fmt.Errorf("无法创建分类保存路径: %w", err)
	}
	category := model.StorageCategory{ID: newID("category"), Name: name, ParentID: parentID, DownloadPath: downloadPath, CreatedAt: time.Now().Format(time.RFC3339)}
	err = m.store.Update(func(state *model.State) error {
		if parentID != "" && !categoryExists(state.Categories, parentID) {
			return errors.New("父分类不存在")
		}
		if siblingCategoryExists(state.Categories, "", parentID, name) {
			return errors.New("同一层级已经存在同名分类")
		}
		state.Categories = append(state.Categories, category)
		return nil
	})
	return category, err
}

func (m *Manager) UpdateCategory(id, name, parentID, downloadPath string) (model.StorageCategory, error) {
	id, name, parentID = strings.TrimSpace(id), strings.TrimSpace(name), strings.TrimSpace(parentID)
	downloadPath, err := normalizeCategoryPath(downloadPath)
	if err != nil {
		return model.StorageCategory{}, err
	}
	if name == "" {
		return model.StorageCategory{}, errors.New("分类名称不能为空")
	}
	if id == parentID {
		return model.StorageCategory{}, errors.New("分类不能作为自己的父分类")
	}
	if err := os.MkdirAll(downloadPath, 0o755); err != nil {
		return model.StorageCategory{}, fmt.Errorf("无法创建分类保存路径: %w", err)
	}
	var updated model.StorageCategory
	err = m.store.Update(func(state *model.State) error {
		index := -1
		for i := range state.Categories {
			if state.Categories[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return errors.New("分类不存在")
		}
		if parentID != "" && !categoryExists(state.Categories, parentID) {
			return errors.New("父分类不存在")
		}
		parents := make(map[string]string, len(state.Categories))
		for _, category := range state.Categories {
			parents[category.ID] = category.ParentID
		}
		seen := map[string]bool{}
		for current := parentID; current != ""; current = parents[current] {
			if current == id {
				return errors.New("不能把分类移动到自己的子分类中")
			}
			if seen[current] {
				return errors.New("分类层级存在循环")
			}
			seen[current] = true
		}
		if siblingCategoryExists(state.Categories, id, parentID, name) {
			return errors.New("同一层级已经存在同名分类")
		}
		state.Categories[index].Name = name
		state.Categories[index].ParentID = parentID
		state.Categories[index].DownloadPath = downloadPath
		updated = state.Categories[index]
		return nil
	})
	return updated, err
}

func (m *Manager) DeleteCategory(id string) error {
	return m.store.Update(func(state *model.State) error {
		if !categoryExists(state.Categories, id) {
			return errors.New("分类不存在")
		}
		if len(state.Categories) == 1 {
			return errors.New("至少需要保留一个分类")
		}
		for _, category := range state.Categories {
			if category.ParentID == id {
				return errors.New("请先删除或移动这个分类的子分类")
			}
		}
		for _, subscription := range state.Subscriptions {
			if subscription.StorageCategoryID == id {
				return errors.New("这个分类仍有订阅，不能删除")
			}
		}
		for _, task := range state.Downloads {
			if task.StorageCategoryID == id && task.Status != "cancelled" && task.Status != "failed" {
				return errors.New("这个分类仍有直接下载任务，不能删除")
			}
		}
		categories := state.Categories[:0]
		for _, category := range state.Categories {
			if category.ID != id {
				categories = append(categories, category)
			}
		}
		state.Categories = categories
		return nil
	})
}

func (m *Manager) AddSubscription(ctx context.Context, rawURL, storageCategoryID string, downloadExisting bool) (model.Subscription, error) {
	storageCategoryID = strings.TrimSpace(storageCategoryID)
	if !categoryExists(m.store.Snapshot().Categories, storageCategoryID) {
		return model.Subscription{}, errors.New("请选择有效的保存分类")
	}
	series, err := m.cctv.ResolveSeries(ctx, rawURL)
	if err != nil {
		return model.Subscription{}, err
	}
	now := time.Now().Format(time.RFC3339)
	sub := model.Subscription{
		ID: newID("sub"), StorageCategoryID: storageCategoryID, Title: series.Title, SourceURL: series.SourceURL, Poster: series.Poster,
		Year: series.Year, Category: series.Category, Region: series.Region, Description: series.Description,
		Status: "active", AutoDownload: true, EpisodeCount: len(series.Episodes), AddedAt: now, LastCheckedAt: now, ArchiveScanned: true,
	}
	err = m.store.Update(func(state *model.State) error {
		if !categoryExists(state.Categories, storageCategoryID) {
			return errors.New("选择的保存分类已不存在")
		}
		for _, existing := range state.Subscriptions {
			if existing.SourceURL == sub.SourceURL {
				return errors.New("这个剧集已经订阅了")
			}
		}
		state.Subscriptions = append([]model.Subscription{sub}, state.Subscriptions...)
		newTasks := make([]model.DownloadTask, 0, len(series.Episodes))
		for _, episode := range series.Episodes {
			episode.SubscriptionID = sub.ID
			episode.ID = episodeID(sub.ID, episode.PageURL)
			state.Episodes = append(state.Episodes, episode)
			if downloadExisting {
				task := newTask(sub, episode, now)
				if state.Settings.DownloadsPaused {
					task.Status = "paused"
				}
				newTasks = append(newTasks, task)
			}
		}
		state.Downloads = append(newTasks, state.Downloads...)
		return nil
	})
	if err != nil {
		return model.Subscription{}, err
	}
	m.Notify()
	return sub, nil
}

// QueueEpisodes adds only explicitly selected catalog entries, without a subscription.
// Validate the selection against fresh CCTV metadata so callers cannot enqueue
// arbitrary URLs or overwrite storage paths.
func (m *Manager) QueueEpisodes(ctx context.Context, rawURL string, chosenURLs []string, storageCategoryID string) (int, int, error) {
	if len(chosenURLs) == 0 || len(chosenURLs) > 200 {
		return 0, 0, errors.New("请选择 1 到 200 集进行下载")
	}
	if !categoryExists(m.store.Snapshot().Categories, storageCategoryID) {
		return 0, 0, errors.New("请选择有效的保存分类")
	}
	series, err := m.cctv.ResolveLink(ctx, rawURL)
	if err != nil {
		return 0, 0, err
	}
	allowed := make(map[string]model.Episode, len(series.Episodes)*2)
	for _, episode := range series.Episodes {
		// Only allow CCTV playback pages or video GUIDs returned from the
		// freshly resolved official program, never arbitrary client URLs.
		u, parseErr := url.Parse(episode.PageURL)
		if parseErr == nil && u.Hostname() == "tv.cctv.com" &&
			(u.Scheme == "https" || u.Scheme == "http") && u.Port() == "" && u.User == nil {
			allowed[episode.PageURL] = episode
		}
		if len(episode.VideoGUID) == 32 {
			if _, hexErr := hex.DecodeString(episode.VideoGUID); hexErr == nil {
				allowed[episode.VideoGUID] = episode
			}
		}
	}
	selection := make([]model.Episode, 0, len(chosenURLs))
	seen := make(map[string]bool, len(chosenURLs))
	skipped := 0
	for _, chosenID := range chosenURLs {
		episode, ok := allowed[chosenID]
		if !ok {
			return 0, 0, errors.New("所选视频不属于该央视节目，请重新解析链接")
		}
		key := episode.VideoGUID
		if key == "" {
			key = episode.PageURL
		}
		if seen[key] {
			skipped++
			continue
		}
		seen[key] = true
		selection = append(selection, episode)
	}
	added, duplicateCount, err := m.queueAdHocEpisodes(series.Title, series.Poster, selection, storageCategoryID)
	return added, skipped + duplicateCount, err
}

// QueueLibraryEpisodes validates every selected video GUID against the
// current official CCTV API page before adding it to the download queue.
func (m *Manager) QueueLibraryEpisodes(ctx context.Context, kind, id, title string, page int, guids []string, categoryID string) (int, int, error) {
	if len(guids) == 0 || len(guids) > 100 {
		return 0, 0, errors.New("请从当前页面选择 1 到 100 个视频")
	}
	browse, err := m.cctv.BrowseLibraryEpisodes(ctx, kind, id, page)
	if err != nil {
		return 0, 0, err
	}
	allowed := make(map[string]model.Episode, len(browse.Episodes))
	for _, episode := range browse.Episodes {
		allowed[episode.VideoGUID] = episode
	}
	var selected []model.Episode
	seen := make(map[string]bool, len(guids))
	skipped := 0
	for _, guid := range guids {
		episode, ok := allowed[guid]
		if !ok {
			return 0, 0, errors.New("所选视频不属于当前央视栏目，请重新加载列表")
		}
		if seen[guid] {
			skipped++
			continue
		}
		seen[guid] = true
		selected = append(selected, episode)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "央视节目"
	}
	if len([]rune(title)) > 100 {
		return 0, 0, errors.New("节目名称过长")
	}
	added, duplicates, err := m.queueAdHocEpisodes(title, "", selected, categoryID)
	return added, skipped + duplicates, err
}

func (m *Manager) queueAdHocEpisodes(seriesTitle, fallbackPoster string, selection []model.Episode, categoryID string) (int, int, error) {
	added, skipped := 0, 0
	now := time.Now().Format(time.RFC3339)
	err := m.store.Update(func(state *model.State) error {
		if !categoryExists(state.Categories, categoryID) {
			return errors.New("所选保存分类已不存在")
		}
		episodeURLs := make(map[string]string, len(state.Episodes))
		for _, episode := range state.Episodes {
			episodeURLs[episode.ID] = episode.PageURL
		}
		existingURLs := make(map[string]bool)
		existingGUIDs := make(map[string]bool)
		for _, task := range state.Downloads {
			if task.Status == "failed" || task.Status == "cancelled" {
				continue
			}
			pageURL := task.PageURL
			if pageURL == "" {
				pageURL = episodeURLs[task.EpisodeID]
			}
			if pageURL != "" {
				existingURLs[pageURL] = true
			}
			if task.VideoGUID != "" {
				existingGUIDs[task.VideoGUID] = true
			}
		}
		tasks := make([]model.DownloadTask, 0, len(selection))
		for _, episode := range selection {
			if episode.PageURL != "" && existingURLs[episode.PageURL] || episode.VideoGUID != "" && existingGUIDs[episode.VideoGUID] {
				skipped++
				continue
			}
			if episode.PageURL != "" {
				existingURLs[episode.PageURL] = true
			}
			if episode.VideoGUID != "" {
				existingGUIDs[episode.VideoGUID] = true
			}
			poster := episode.Image
			if poster == "" {
				poster = fallbackPoster
			}
			task := model.DownloadTask{
				ID: newID("task"), Title: episode.Title, SeriesTitle: seriesTitle,
				Poster: poster, PageURL: episode.PageURL, VideoGUID: episode.VideoGUID,
				StorageCategoryID: categoryID, Status: "queued", CreatedAt: now,
			}
			if state.Settings.DownloadsPaused {
				task.Status = "paused"
			}
			tasks = append(tasks, task)
		}
		added = len(tasks)
		state.Downloads = append(tasks, state.Downloads...)
		return nil
	})
	if err == nil && added > 0 {
		m.Notify()
	}
	return added, skipped, err
}

func (m *Manager) CheckSubscription(ctx context.Context, id string) error {
	m.mu.Lock()
	if m.checking[id] {
		m.mu.Unlock()
		return errors.New("正在检查这个订阅")
	}
	m.checking[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.checking, id)
		m.mu.Unlock()
	}()

	snapshot := m.store.Snapshot()
	var current model.Subscription
	for _, sub := range snapshot.Subscriptions {
		if sub.ID == id {
			current = sub
			break
		}
	}
	if current.ID == "" {
		return errors.New("订阅不存在")
	}
	archiveBackfill := !current.ArchiveScanned
	var series model.Series
	var err error
	if !archiveBackfill {
		series, err = m.cctv.ResolveRecentSeries(ctx, current.SourceURL)
	} else {
		// One-time backfill for subscriptions created before the full column
		// archive parser existed. Do not mark it complete unless the scan succeeds.
		series, err = m.cctv.ResolveSeries(ctx, current.SourceURL)
	}
	if err != nil {
		_ = m.store.Update(func(state *model.State) error {
			for i := range state.Subscriptions {
				if state.Subscriptions[i].ID == id {
					state.Subscriptions[i].LastCheckedAt = time.Now().Format(time.RFC3339)
					state.Subscriptions[i].LastError = err.Error()
				}
			}
			return nil
		})
		return err
	}

	now := time.Now().Format(time.RFC3339)
	err = m.store.Update(func(state *model.State) error {
		existingURLs := map[string]bool{}
		existingGUIDs := map[string]bool{}
		for _, episode := range state.Episodes {
			if episode.SubscriptionID == id {
				if episode.PageURL != "" {
					existingURLs[episode.PageURL] = true
				}
				if episode.VideoGUID != "" {
					existingGUIDs[strings.ToLower(episode.VideoGUID)] = true
				}
			}
		}
		var sub *model.Subscription
		for i := range state.Subscriptions {
			if state.Subscriptions[i].ID == id {
				sub = &state.Subscriptions[i]
				break
			}
		}
		if sub == nil {
			return errors.New("订阅不存在")
		}
		sub.Title, sub.Poster, sub.Description = series.Title, series.Poster, series.Description
		sub.Year, sub.Category, sub.Region = series.Year, series.Category, series.Region
		sub.LastCheckedAt, sub.LastError = now, ""
		sub.ArchiveScanned = true
		newTasks := make([]model.DownloadTask, 0)
		for _, episode := range series.Episodes {
			guid := strings.ToLower(episode.VideoGUID)
			if (episode.PageURL != "" && existingURLs[episode.PageURL]) || (guid != "" && existingGUIDs[guid]) {
				continue
			}
			episode.SubscriptionID = id
			episode.ID = episodeID(id, episode.PageURL)
			state.Episodes = append(state.Episodes, episode)
			if episode.PageURL != "" {
				existingURLs[episode.PageURL] = true
			}
			if guid != "" {
				existingGUIDs[guid] = true
			}
			// Historical episodes discovered during a one-time backfill are
			// added to the management list but are not automatically queued.
			// Routine future checks still honor the subscription's auto-download
			// setting for newly released episodes.
			if !archiveBackfill && sub.AutoDownload && sub.Status == "active" {
				task := newTask(*sub, episode, now)
				if state.Settings.DownloadsPaused {
					task.Status = "paused"
				}
				newTasks = append(newTasks, task)
			}
		}
		state.Downloads = append(newTasks, state.Downloads...)
		sub.EpisodeCount = 0
		for _, episode := range state.Episodes {
			if episode.SubscriptionID == id {
				sub.EpisodeCount++
			}
		}
		return nil
	})
	if err == nil {
		m.Notify()
	}
	return err
}

func (m *Manager) CheckSubscriptionAsync(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_ = m.CheckSubscription(ctx, id)
	}()
}

func (m *Manager) UpdateSubscription(id, status string, autoDownload *bool, storageCategoryID *string) error {
	return m.store.Update(func(state *model.State) error {
		for i := range state.Subscriptions {
			if state.Subscriptions[i].ID != id {
				continue
			}
			if status != "" {
				if status != "active" && status != "paused" {
					return errors.New("订阅状态无效")
				}
				state.Subscriptions[i].Status = status
			}
			if autoDownload != nil {
				state.Subscriptions[i].AutoDownload = *autoDownload
			}
			if storageCategoryID != nil {
				categoryID := strings.TrimSpace(*storageCategoryID)
				if !categoryExists(state.Categories, categoryID) {
					return errors.New("保存分类不存在")
				}
				state.Subscriptions[i].StorageCategoryID = categoryID
			}
			return nil
		}
		return errors.New("订阅不存在")
	})
}

func (m *Manager) DeleteSubscription(id string) error {
	m.mu.Lock()
	for taskID, cancel := range m.active {
		snapshot := m.store.Snapshot()
		for _, task := range snapshot.Downloads {
			if task.ID == taskID && task.SubscriptionID == id {
				cancel()
			}
		}
	}
	m.mu.Unlock()
	return m.store.Update(func(state *model.State) error {
		found := false
		subs := state.Subscriptions[:0]
		for _, sub := range state.Subscriptions {
			if sub.ID == id {
				found = true
				continue
			}
			subs = append(subs, sub)
		}
		if !found {
			return errors.New("订阅不存在")
		}
		state.Subscriptions = subs
		episodes := state.Episodes[:0]
		for _, episode := range state.Episodes {
			if episode.SubscriptionID != id {
				episodes = append(episodes, episode)
			}
		}
		state.Episodes = episodes
		downloads := state.Downloads[:0]
		for _, task := range state.Downloads {
			if task.SubscriptionID != id {
				downloads = append(downloads, task)
			}
		}
		state.Downloads = downloads
		return nil
	})
}

func (m *Manager) RetryTask(id string) error {
	err := m.store.Update(func(state *model.State) error {
		for i := range state.Downloads {
			if state.Downloads[i].ID == id {
				if state.Downloads[i].Status != "failed" && state.Downloads[i].Status != "cancelled" && state.Downloads[i].Status != "completed" {
					return errors.New("只有失败、已取消或已完成的任务可以重试")
				}
				if state.Settings.DownloadsPaused {
					state.Downloads[i].Status = "paused"
				} else {
					state.Downloads[i].Status = "queued"
				}
				state.Downloads[i].Progress = 0
				state.Downloads[i].Error = ""
				state.Downloads[i].Speed = ""
				state.Downloads[i].VideoQuality = ""
				state.Downloads[i].CompletedAt = ""
				recountDownloaded(state, state.Downloads[i].SubscriptionID)
				return nil
			}
		}
		return errors.New("下载任务不存在")
	})
	if err == nil {
		m.Notify()
	}
	return err
}

func (m *Manager) CancelTask(id string) error {
	m.mu.Lock()
	cancel := m.active[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil
	}
	return m.store.Update(func(state *model.State) error {
		for i := range state.Downloads {
			if state.Downloads[i].ID == id {
				if state.Downloads[i].Status != "queued" && state.Downloads[i].Status != "paused" {
					return errors.New("当前任务不能取消")
				}
				state.Downloads[i].Status = "cancelled"
				state.Downloads[i].CompletedAt = time.Now().Format(time.RFC3339)
				return nil
			}
		}
		return errors.New("下载任务不存在")
	})
}

func (m *Manager) PauseAll() error {
	if err := m.store.Update(func(state *model.State) error {
		state.Settings.DownloadsPaused = true
		for i := range state.Downloads {
			if state.Downloads[i].Status == "queued" || state.Downloads[i].Status == "downloading" {
				state.Downloads[i].Status = "paused"
				state.Downloads[i].Progress = 0
				state.Downloads[i].Speed = ""
			}
		}
		return nil
	}); err != nil {
		return err
	}
	m.mu.Lock()
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) ResumeAll() error {
	if err := m.store.Update(func(state *model.State) error {
		state.Settings.DownloadsPaused = false
		for i := range state.Downloads {
			if state.Downloads[i].Status == "paused" {
				state.Downloads[i].Status = "queued"
				state.Downloads[i].Speed = ""
				state.Downloads[i].StartedAt = ""
			}
		}
		return nil
	}); err != nil {
		return err
	}
	m.Notify()
	return nil
}

func (m *Manager) dispatchLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
		m.dispatch(ctx)
	}
}

func (m *Manager) dispatch(root context.Context) {
	snapshot := m.store.Snapshot()
	if snapshot.Settings.DownloadsPaused {
		return
	}
	limit := snapshot.Settings.ConcurrentDownloads
	if limit < 1 {
		limit = 1
	}
	m.mu.Lock()
	available := limit - len(m.active)
	if available <= 0 {
		m.mu.Unlock()
		return
	}
	for _, task := range snapshot.Downloads {
		if available == 0 {
			break
		}
		if task.Status != "queued" {
			continue
		}
		taskCtx, cancel := context.WithCancel(root)
		m.active[task.ID] = cancel
		available--
		go m.runTask(taskCtx, task.ID)
	}
	m.mu.Unlock()
}

func (m *Manager) runTask(ctx context.Context, id string) {
	defer func() {
		m.mu.Lock()
		delete(m.active, id)
		m.mu.Unlock()
		m.Notify()
	}()
	started := time.Now()
	if err := m.store.Update(func(state *model.State) error {
		if state.Settings.DownloadsPaused {
			return errors.New("下载已全部暂停")
		}
		for i := range state.Downloads {
			if state.Downloads[i].ID == id {
				if state.Downloads[i].Status != "queued" {
					return errors.New("任务不在等待队列")
				}
				state.Downloads[i].Status, state.Downloads[i].StartedAt, state.Downloads[i].Error = "downloading", started.Format(time.RFC3339), ""
				return nil
			}
		}
		return errors.New("下载任务不存在")
	}); err != nil {
		return
	}

	snapshot := m.store.Snapshot()
	var task model.DownloadTask
	var episode model.Episode
	var sub model.Subscription
	var category model.StorageCategory
	for _, item := range snapshot.Downloads {
		if item.ID == id {
			task = item
			break
		}
	}
	for _, item := range snapshot.Episodes {
		if item.ID == task.EpisodeID {
			episode = item
			break
		}
	}
	for _, item := range snapshot.Subscriptions {
		if item.ID == task.SubscriptionID {
			sub = item
			break
		}
	}
	if task.SubscriptionID == "" && (task.PageURL != "" || task.VideoGUID != "") {
		episode = model.Episode{PageURL: task.PageURL, VideoGUID: task.VideoGUID, Title: task.Title, Image: task.Poster}
		sub = model.Subscription{Title: task.SeriesTitle, StorageCategoryID: task.StorageCategoryID}
	}
	for _, item := range snapshot.Categories {
		if item.ID == sub.StorageCategoryID {
			category = item
			break
		}
	}
	if episode.PageURL == "" && episode.VideoGUID == "" || sub.Title == "" {
		m.failTask(id, errors.New("下载任务关联的视频数据不存在"))
		return
	}
	if category.ID == "" || strings.TrimSpace(category.DownloadPath) == "" {
		m.failTask(id, errors.New("订阅关联的保存分类不存在或未设置路径"))
		return
	}
	var video cctv.VideoInfo
	var err error
	if episode.VideoGUID != "" {
		video, err = m.cctv.ResolveVideoByGUID(ctx, episode.VideoGUID)
	} else {
		video, err = m.cctv.ResolveVideo(ctx, episode.PageURL)
	}
	if err != nil {
		m.finishFromError(id, err)
		return
	}
	dir := seriesDownloadDir(category.DownloadPath, sub.Title)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.failTask(id, fmt.Errorf("创建下载目录: %w", err))
		return
	}
	base := sanitizeFilename(episode.Title)
	if base == "" {
		base = fmt.Sprintf("第%d集", episode.Number)
	}
	finalPath := filepath.Join(dir, base+".mp4")
	tempPath := finalPath + ".part"
	err = m.downloadHLS(ctx, id, video.HLSURLs, tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		m.finishFromError(id, err)
		return
	}
	if err := replaceDownloadedFile(tempPath, finalPath); err != nil {
		m.failTask(id, fmt.Errorf("保存文件: %w", err))
		return
	}
	now := time.Now().Format(time.RFC3339)
	_ = m.store.Update(func(state *model.State) error {
		for i := range state.Downloads {
			if state.Downloads[i].ID == id {
				state.Downloads[i].Status = "completed"
				state.Downloads[i].Progress = 100
				state.Downloads[i].Speed = ""
				state.Downloads[i].FilePath = finalPath
				state.Downloads[i].CompletedAt = now
			}
		}
		recountDownloaded(state, sub.ID)
		return nil
	})
}

func (m *Manager) downloadHLS(ctx context.Context, taskID string, playlistURLs []string, output string) error {
	selected, err := m.choosePlayableHLS(ctx, playlistURLs)
	if err != nil {
		return err
	}
	_ = m.updateTask(taskID, func(task *model.DownloadTask) {
		task.VideoQuality = selected.quality
		task.Speed = "已检测可播放的视频源，准备下载 " + selected.quality
	})
	if err := m.runHLSMedia(ctx, taskID, selected.playlist.Segments, output, selected.useWASM, selected.useNative); err != nil {
		return err
	}
	_ = m.updateTask(taskID, func(task *model.DownloadTask) {
		task.Progress = 96
		task.Speed = "正在确认视频画面可解码"
	})
	if err := checkVideoFilePlayable(ctx, output); err != nil {
		return fmt.Errorf("MP4 画面验证失败，文件未标记为完成: %w", err)
	}
	if selected.useNative || m.store.Snapshot().Settings.VerifyVideoAfterDownload {
		_ = m.updateTask(taskID, func(task *model.DownloadTask) {
			task.Progress = 97
			task.Speed = "正在进行全片完整性校验"
		})
		return verifyDecodedVideo(ctx, output)
	}
	_ = m.updateTask(taskID, func(task *model.DownloadTask) {
		task.Progress = 99
		task.Speed = "正在保存文件"
	})
	return nil
}

func (m *Manager) runHLSMedia(ctx context.Context, taskID string, segments []hlsSegment, output string, decryptWithWASM, decryptWithNative bool) error {
	var nativeTool string
	if decryptWithNative {
		var err error
		nativeTool, err = findNativeH5E()
		if err != nil {
			return fmt.Errorf("未找到央视 H5E 原生解密组件: %w", err)
		}
	}
	var engine *cctvwasm.Engine
	if decryptWithWASM {
		var err error
		engine, err = m.cctvDecryptorEngine(ctx)
		if err != nil {
			return err
		}
	}

	clearFile, err := os.CreateTemp(filepath.Dir(output), ".cctv-clear-*.ts")
	if err != nil {
		return fmt.Errorf("创建解密临时文件: %w", err)
	}
	clearPath := clearFile.Name()
	defer os.Remove(clearPath)
	closed := false
	defer func() {
		if !closed {
			_ = clearFile.Close()
		}
	}()

	keys := make(map[string][]byte)
	started := time.Now()
	downloadedBytes := int64(0)
	parallelism := m.store.Snapshot().Settings.SegmentConcurrency
	if err := fetchSegmentsInOrder(ctx, m.cctv.HTTPClient(), segments, parallelism, func(index int, data []byte) error {
		segment := segments[index]
		if err := ctx.Err(); err != nil {
			return err
		}
		downloadedBytes += int64(len(data))
		if segment.KeyURL != "" {
			key := keys[segment.KeyURL]
			if key == nil {
				var err error
				key, err = fetchBytes(ctx, m.cctv.HTTPClient(), segment.KeyURL)
				if err != nil {
					return fmt.Errorf("下载 HLS 密钥: %w", err)
				}
				keys[segment.KeyURL] = key
			}
			var err error
			data, err = decryptAES128(data, key, segment.IV)
			if err != nil {
				return fmt.Errorf("解密 HLS 分片 %d/%d: %w", index+1, len(segments), err)
			}
		}
		clear := data
		var err error
		if decryptWithNative {
			clear, err = nativeH5EDecryptTS(ctx, nativeTool, data)
			if err != nil {
				return fmt.Errorf("原生 H5E 解密分片 %d/%d: %w", index+1, len(segments), err)
			}
		} else if engine != nil {
			mediaTagID := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), index)
			clear, err = engine.DecryptMPEGTS(ctx, data, mediaTagID, cctvPlayerOrigin)
			if err != nil {
				return fmt.Errorf("纯 Go 解密视频分片 %d/%d: %w", index+1, len(segments), err)
			}
		}
		if _, err := clearFile.Write(clear); err != nil {
			return fmt.Errorf("写入解密分片 %d/%d: %w", index+1, len(segments), err)
		}
		elapsed := time.Since(started).Seconds()
		speed := ""
		if elapsed > 0 {
			speed = formatSpeed(float64(downloadedBytes) / elapsed)
		}
		progress := 2 + float64(index+1)/float64(len(segments))*90
		_ = m.updateTask(taskID, func(task *model.DownloadTask) {
			task.Progress = progress
			method := "直接下载可播放画质"
			if decryptWithWASM {
				method = "纯 Go 解密视频"
			} else if decryptWithNative {
				method = "原生 H5E 高清解密"
			}
			task.Speed = fmt.Sprintf("%d 路并发 · %s %d/%d · %s", parallelism, method, index+1, len(segments), speed)
		})
		return nil
	}); err != nil {
		return err
	}
	if err := clearFile.Close(); err != nil {
		return fmt.Errorf("完成解密临时文件: %w", err)
	}
	closed = true
	_ = m.updateTask(taskID, func(task *model.DownloadTask) {
		task.Progress = 94
		task.Speed = "FFmpeg 正在无损封装（copy）"
	})
	if err := remuxMPEGTS(ctx, clearPath, output); err != nil {
		return err
	}
	return nil
}

func (m *Manager) cctvDecryptorEngine(ctx context.Context) (*cctvwasm.Engine, error) {
	// Compiling CCTV's large official module is CPU intensive. Reuse one
	// compiled engine for the whole process; Engine serializes its short-lived
	// per-segment sessions while network downloads remain concurrent.
	m.engineMu.Lock()
	defer m.engineMu.Unlock()
	if m.engine != nil {
		return m.engine, nil
	}
	workerSource, err := m.loadCCTVWorkerSource(ctx)
	if err != nil {
		return nil, err
	}
	engine, err := cctvwasm.NewEngine(ctx, workerSource)
	if err != nil {
		return nil, fmt.Errorf("初始化纯 Go 央视解密器: %w", err)
	}
	m.engine = engine
	return engine, nil
}

func (m *Manager) loadCCTVWorkerSource(ctx context.Context) ([]byte, error) {
	m.workerMu.Lock()
	defer m.workerMu.Unlock()
	if len(m.workerSource) > 0 && time.Since(m.workerLoadedAt) < cctvWorkerCacheAge {
		return append([]byte(nil), m.workerSource...), nil
	}
	if explicit := strings.TrimSpace(os.Getenv("CCTV_WASM_SOURCE_PATH")); explicit != "" {
		source, err := os.ReadFile(explicit)
		if err != nil {
			return nil, fmt.Errorf("读取 CCTV_WASM_SOURCE_PATH: %w", err)
		}
		m.workerSource, m.workerLoadedAt = append([]byte(nil), source...), time.Now()
		return append([]byte(nil), source...), nil
	}
	cacheDirectory := strings.TrimSpace(os.Getenv("CCTV_PLAYER_CACHE"))
	if cacheDirectory == "" {
		cacheDirectory = filepath.Join(os.TempDir(), "cctv-player-cache")
	}
	cachePath := filepath.Join(cacheDirectory, "cctv.worker.js")
	if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < cctvWorkerCacheAge {
		if source, readErr := os.ReadFile(cachePath); readErr == nil {
			m.workerSource, m.workerLoadedAt = append([]byte(nil), source...), info.ModTime()
			return append([]byte(nil), source...), nil
		}
	}
	source, fetchErr := fetchBytes(ctx, m.cctv.HTTPClient(), cctvWorkerURL)
	if fetchErr != nil {
		if stale, readErr := os.ReadFile(cachePath); readErr == nil {
			m.workerSource, m.workerLoadedAt = append([]byte(nil), stale...), time.Now()
			return append([]byte(nil), stale...), nil
		}
		return nil, fmt.Errorf("下载央视官方 WASM 模块: %w", fetchErr)
	}
	if err := os.MkdirAll(cacheDirectory, 0o755); err != nil {
		return nil, fmt.Errorf("创建央视模块缓存: %w", err)
	}
	temporary, err := os.CreateTemp(cacheDirectory, ".cctv-worker-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("创建央视模块缓存文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(source); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("写入央视模块缓存: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, cachePath); err != nil {
		return nil, fmt.Errorf("保存央视模块缓存: %w", err)
	}
	m.workerSource, m.workerLoadedAt = append([]byte(nil), source...), time.Now()
	return append([]byte(nil), source...), nil
}

func remuxMPEGTS(ctx context.Context, input, output string) error {
	ffmpegPath, err := ffmpeg.Find()
	if err != nil {
		return fmt.Errorf("未找到 FFmpeg，无法封装 MP4；请将 ffmpeg.exe 放到程序同目录或设置 CCTV_FFMPEG_PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin", "-hide_banner", "-v", "error", "-y",
		"-i", input, "-map", "0:v:0", "-map", "0:a?",
		"-c", "copy", "-movflags", "+faststart", "-f", "mp4", output,
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(combined))
		if len(message) > 1200 {
			message = message[len(message)-1200:]
		}
		return fmt.Errorf("FFmpeg 无损封装失败: %s", message)
	}
	return nil
}

func verifyDecodedVideo(ctx context.Context, filename string) error {
	ffmpegPath, err := ffmpeg.Find()
	if err != nil {
		return fmt.Errorf("未找到 FFmpeg，无法验证下载视频；请将 ffmpeg.exe 放到程序同目录或设置 CCTV_FFMPEG_PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-v", "error", "-xerror", "-i", filename, "-map", "0:v:0", "-f", "null", "-")
	output, err := cmd.CombinedOutput()
	if err != nil || len(bytes.TrimSpace(output)) > 0 {
		message := strings.TrimSpace(string(output))
		if len(message) > 1000 {
			message = message[len(message)-1000:]
		}
		if message == "" {
			message = "视频解码器返回错误"
		}
		return fmt.Errorf("最高画质视频完整性验证失败: %s", message)
	}
	return nil
}

func (m *Manager) finishFromError(id string, err error) {
	if errors.Is(err, context.Canceled) {
		_ = m.store.Update(func(state *model.State) error {
			for i := range state.Downloads {
				if state.Downloads[i].ID != id {
					continue
				}
				if state.Settings.DownloadsPaused || state.Downloads[i].Status == "paused" {
					state.Downloads[i].Status, state.Downloads[i].Speed = "paused", ""
					return nil
				}
				state.Downloads[i].Status, state.Downloads[i].Speed, state.Downloads[i].CompletedAt = "cancelled", "", time.Now().Format(time.RFC3339)
				return nil
			}
			return nil
		})
		return
	}
	m.failTask(id, err)
}

func (m *Manager) failTask(id string, err error) {
	_ = m.updateTask(id, func(task *model.DownloadTask) {
		task.Status, task.Speed, task.Error = "failed", "", err.Error()
		task.CompletedAt = time.Now().Format(time.RFC3339)
	})
}

func (m *Manager) updateTask(id string, fn func(*model.DownloadTask)) error {
	return m.store.Update(func(state *model.State) error {
		for i := range state.Downloads {
			if state.Downloads[i].ID == id {
				fn(&state.Downloads[i])
				return nil
			}
		}
		return errors.New("下载任务不存在")
	})
}

func (m *Manager) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastPoll := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		snapshot := m.store.Snapshot()
		interval := time.Duration(snapshot.Settings.PollIntervalMinutes) * time.Minute
		if time.Since(lastPoll) < interval {
			continue
		}
		lastPoll = time.Now()
		for _, sub := range snapshot.Subscriptions {
			if sub.Status == "active" {
				m.CheckSubscriptionAsync(sub.ID)
			}
		}
	}
}

type mediaPlaylist struct{ Segments []hlsSegment }
type hlsSegment struct {
	URL, KeyURL string
	IV          []byte
}

var bandwidthRE = regexp.MustCompile(`BANDWIDTH=(\d+)`)
var resolutionRE = regexp.MustCompile(`RESOLUTION=(\d+)x(\d+)`)
var keyURIRe = regexp.MustCompile(`URI="([^"]+)"`)
var keyIVRe = regexp.MustCompile(`IV=0x([0-9a-fA-F]+)`)

func loadMediaPlaylist(ctx context.Context, client *http.Client, rawURL string, depth int) (mediaPlaylist, error) {
	if depth > 4 {
		return mediaPlaylist{}, errors.New("HLS 播放列表嵌套过深")
	}
	body, err := fetchBytes(ctx, client, rawURL)
	if err != nil {
		return mediaPlaylist{}, fmt.Errorf("读取 HLS 播放列表: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	type variant struct {
		bandwidth int
		width     int
		height    int
		uri       string
	}
	var variants []variant
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") && i+1 < len(lines) {
			bw := 0
			if match := bandwidthRE.FindStringSubmatch(line); len(match) == 2 {
				bw, _ = strconv.Atoi(match[1])
			}
			width, height := 0, 0
			if match := resolutionRE.FindStringSubmatch(line); len(match) == 3 {
				width, _ = strconv.Atoi(match[1])
				height, _ = strconv.Atoi(match[2])
			}
			variants = append(variants, variant{bandwidth: bw, width: width, height: height, uri: resolveURL(rawURL, strings.TrimSpace(lines[i+1]))})
		}
	}
	if len(variants) > 0 {
		sort.SliceStable(variants, func(i, j int) bool {
			leftPixels := variants[i].width * variants[i].height
			rightPixels := variants[j].width * variants[j].height
			if leftPixels != rightPixels {
				return leftPixels > rightPixels
			}
			if variants[i].height != variants[j].height {
				return variants[i].height > variants[j].height
			}
			return variants[i].bandwidth > variants[j].bandwidth
		})
		return loadMediaPlaylist(ctx, client, variants[0].uri, depth+1)
	}

	playlist := mediaPlaylist{}
	keyURL := ""
	var keyIV []byte
	sequence := 0
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			sequence, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			if strings.Contains(line, "METHOD=NONE") {
				keyURL, keyIV = "", nil
				continue
			}
			if !strings.Contains(line, "METHOD=AES-128") {
				return mediaPlaylist{}, errors.New("暂不支持这个 HLS 加密方式")
			}
			match := keyURIRe.FindStringSubmatch(line)
			if len(match) != 2 {
				return mediaPlaylist{}, errors.New("HLS 密钥地址缺失")
			}
			keyURL = resolveURL(rawURL, match[1])
			keyIV = nil
			if ivMatch := keyIVRe.FindStringSubmatch(line); len(ivMatch) == 2 {
				keyIV, _ = hex.DecodeString(padLeft(ivMatch[1], 32))
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		iv := append([]byte(nil), keyIV...)
		if keyURL != "" && len(iv) == 0 {
			iv = make([]byte, aes.BlockSize)
			n := sequence + len(playlist.Segments)
			for i := len(iv) - 1; i >= 0 && n > 0; i-- {
				iv[i] = byte(n)
				n >>= 8
			}
		}
		playlist.Segments = append(playlist.Segments, hlsSegment{URL: resolveURL(rawURL, line), KeyURL: keyURL, IV: iv})
	}
	return playlist, nil
}

func fetchBytes(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://tv.cctv.com/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func decryptAES128(data, key, iv []byte) ([]byte, error) {
	if len(key) != 16 || len(iv) != aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("AES-128 分片参数不正确")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	if len(out) > 0 {
		padding := int(out[len(out)-1])
		if padding > 0 && padding <= aes.BlockSize && padding <= len(out) {
			valid := true
			for _, b := range out[len(out)-padding:] {
				if int(b) != padding {
					valid = false
					break
				}
			}
			if valid {
				out = out[:len(out)-padding]
			}
		}
	}
	return out, nil
}

var cctvPlayerMagic = []byte{0xde, 0xad, 0xbe, 0xef}

// decryptCCTVPlayerPayload mirrors the envelope used by vhs_drm2.min.js. The
// player wraps transmuxed video bytes with a DE AD BE EF header, hides a
// transformed 32-byte key in the middle of the payload, and stores a
// transformed 16-byte IV at the end. Some CCTV delivery paths expose that
// envelope directly; clear TS segments do not and pass through unchanged.
func decryptCCTVPlayerPayload(data []byte) ([]byte, bool, error) {
	offset := bytes.Index(data, cctvPlayerMagic)
	if offset < 0 {
		return data, false, nil
	}
	if offset > 0 && !looksLikeISOBMFF(data[:offset]) {
		// A random magic sequence inside a normal TS payload is not an envelope.
		return data, false, nil
	}
	clear, err := decryptCCTVPlayerEnvelope(data[offset:])
	if err != nil {
		return nil, true, err
	}
	if offset == 0 {
		return clear, true, nil
	}
	result := make([]byte, 0, offset+len(clear))
	result = append(result, data[:offset]...)
	result = append(result, clear...)
	return result, true, nil
}

func decryptCCTVPlayerEnvelope(data []byte) ([]byte, error) {
	const headerLength = 6
	if len(data) < headerLength+1 || !bytes.Equal(data[:4], cctvPlayerMagic) {
		return nil, errors.New("央视播放器加密头无效")
	}
	keyLength, ivLength := int(data[4]), int(data[5])
	if keyLength <= 0 || ivLength <= 0 || len(data) < headerLength+keyLength+ivLength {
		return nil, errors.New("央视播放器密钥或 IV 长度无效")
	}
	ivOffset := len(data) - ivLength
	encryptedLength := ivOffset - headerLength - keyLength
	if encryptedLength < 0 {
		return nil, errors.New("央视播放器分片长度无效")
	}
	keyOffset := headerLength + encryptedLength/2
	if keyOffset+keyLength > ivOffset {
		return nil, errors.New("央视播放器密钥位置无效")
	}
	transformedKey := data[keyOffset : keyOffset+keyLength]
	transformedIV := data[ivOffset:]
	key := make([]byte, keyLength)
	iv := make([]byte, ivLength)
	keySeed := 37*keyLength + 43*ivLength
	ivSeed := 53*keyLength + 61*ivLength
	for i := range key {
		key[i] = transformedKey[i] ^ byte((keySeed+7*i)%256)
	}
	for i := range iv {
		iv[i] = transformedIV[i] ^ byte((ivSeed+11*i)%256)
	}

	encrypted := make([]byte, 0, encryptedLength)
	encrypted = append(encrypted, data[headerLength:keyOffset]...)
	encrypted = append(encrypted, data[keyOffset+keyLength:ivOffset]...)
	if len(encrypted) == 0 {
		return []byte{}, nil
	}
	step := ((clamp(len(encrypted)/8, 2, 16) + len(encrypted)%7) % 16) + 1
	permutation := make([]int, len(encrypted))
	for i := range permutation {
		permutation[i] = i
	}
	for i := len(permutation) - 1; i > 0; i-- {
		position := (int(key[i%len(key)]) + int(iv[i%len(iv)]) + i) % (i + 1)
		permutation[position], permutation[i] = permutation[i], permutation[position]
	}
	clear := make([]byte, len(encrypted))
	for i, value := range encrypted {
		keyIndex := (i * step) % len(key)
		ivIndex := (i * (step + 1)) % len(iv)
		decoded := int(value) ^ step
		decoded = (decoded - 3*int(key[keyIndex]) + 1024) % 256
		decoded ^= i & 0xff
		decoded ^= int(iv[ivIndex])
		decoded ^= int(key[keyIndex])
		clear[permutation[i]] = byte(decoded)
	}
	return clear, nil
}

func looksLikeMediaSegment(data []byte) bool {
	if looksLikeISOBMFF(data) {
		return true
	}
	for offset := 0; offset < 188 && offset+188*4 < len(data); offset++ {
		valid := true
		for packet := 0; packet < 5; packet++ {
			if data[offset+packet*188] != 0x47 {
				valid = false
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func looksLikeISOBMFF(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	switch string(data[4:8]) {
	case "ftyp", "styp", "moof", "moov", "sidx":
		return true
	default:
		return false
	}
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func resolveURL(baseURL, ref string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(rel).String()
}

func newTask(sub model.Subscription, episode model.Episode, now string) model.DownloadTask {
	return model.DownloadTask{ID: newID("task"), SubscriptionID: sub.ID, EpisodeID: episode.ID, Title: episode.Title, SeriesTitle: sub.Title, Poster: episode.Image, PageURL: episode.PageURL, VideoGUID: episode.VideoGUID, Status: "queued", CreatedAt: now}
}

func recountDownloaded(state *model.State, subID string) {
	completed := map[string]bool{}
	for _, task := range state.Downloads {
		if task.SubscriptionID == subID && task.Status == "completed" {
			completed[task.EpisodeID] = true
		}
	}
	for i := range state.Subscriptions {
		if state.Subscriptions[i].ID == subID {
			state.Subscriptions[i].DownloadedCount = len(completed)
		}
	}
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
func episodeID(subID, pageURL string) string {
	sum := sha1.Sum([]byte(pageURL))
	return subID + "_" + hex.EncodeToString(sum[:8])
}
func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	value = replacer.Replace(value)
	value = strings.Trim(value, ". ")
	if len([]rune(value)) > 120 {
		value = string([]rune(value)[:120])
	}
	return value
}
func seriesDownloadDir(root, seriesTitle string) string {
	name := sanitizeFilename(seriesTitle)
	if name == "" {
		name = "未命名剧集"
	}
	return filepath.Join(root, name)
}
func normalizeCategoryPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("分类保存路径不能为空")
	}
	absPath, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("分类保存路径无效: %w", err)
	}
	return absPath, nil
}
func categoryExists(categories []model.StorageCategory, id string) bool {
	if id == "" {
		return false
	}
	for _, category := range categories {
		if category.ID == id {
			return true
		}
	}
	return false
}
func siblingCategoryExists(categories []model.StorageCategory, excludedID, parentID, name string) bool {
	for _, category := range categories {
		if category.ID != excludedID && category.ParentID == parentID && strings.EqualFold(strings.TrimSpace(category.Name), name) {
			return true
		}
	}
	return false
}
func formatSpeed(bytesPerSecond float64) string {
	if bytesPerSecond >= 1024*1024 {
		return fmt.Sprintf("%.1f MB/s", bytesPerSecond/1024/1024)
	}
	return fmt.Sprintf("%.0f KB/s", bytesPerSecond/1024)
}
func padLeft(value string, length int) string {
	if len(value) >= length {
		return value
	}
	return strings.Repeat("0", length-len(value)) + value
}
