package service

import (
	"errors"
	"fmt"
	"time"

	"cctv-sub/internal/model"
)

// QueueSubscriptionEpisodes queues selected episodes and retries failed,
// cancelled, or completed tasks on explicit user request.
func (m *Manager) QueueSubscriptionEpisodes(subscriptionID string, episodeIDs []string) (int, int, error) {
	if len(episodeIDs) == 0 || len(episodeIDs) > 200 {
		return 0, 0, errors.New("请选择 1 到 200 集")
	}
	selected := make(map[string]bool, len(episodeIDs))
	for _, id := range episodeIDs {
		if id == "" || selected[id] { return 0, 0, errors.New("集数列表包含空 ID 或重复 ID") }
		selected[id] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	added, skipped := 0, 0
	now := time.Now().Format(time.RFC3339)
	err := m.store.Update(func(state *model.State) error {
		var sub *model.Subscription
		for i := range state.Subscriptions { if state.Subscriptions[i].ID == subscriptionID { sub = &state.Subscriptions[i]; break } }
		if sub == nil { return errors.New("订阅不存在") }
		episodes := make(map[string]model.Episode)
		for _, episode := range state.Episodes { if episode.SubscriptionID == subscriptionID { episodes[episode.ID] = episode } }
		if len(episodes) == 0 { return errors.New("这个订阅还没有解析到集数，请先点击立即检查") }
		for id := range selected { if _, ok := episodes[id]; !ok { return fmt.Errorf("集数 %s 不属于这个订阅，请刷新列表", id) } }
		for id := range selected {
			episode := episodes[id]
			taskIndex := -1
			for i := range state.Downloads {
				task := &state.Downloads[i]
				if task.SubscriptionID == subscriptionID && (task.EpisodeID == id || (episode.VideoGUID != "" && task.VideoGUID == episode.VideoGUID) || (episode.PageURL != "" && task.PageURL == episode.PageURL)) {
					if taskIndex < 0 || task.CreatedAt > state.Downloads[taskIndex].CreatedAt { taskIndex = i }
				}
			}
			if taskIndex < 0 {
				task := newTask(*sub, episode, now)
				if state.Settings.DownloadsPaused { task.Status = "paused" }
				state.Downloads = append(state.Downloads, task)
				added++
				continue
			}
			task := &state.Downloads[taskIndex]
			if task.Status == "queued" || task.Status == "downloading" || task.Status == "paused" { skipped++; continue }
			if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
				if state.Settings.DownloadsPaused { task.Status = "paused" } else { task.Status = "queued" }
				task.Progress, task.Error, task.Speed, task.VideoQuality = 0, "", "", ""
				task.CompletedAt, task.StartedAt = "", ""
				added++
			}
		}
		recountDownloaded(state, subscriptionID)
		return nil
	})
	if err == nil && added > 0 { m.Notify() }
	return added, skipped, err
}
