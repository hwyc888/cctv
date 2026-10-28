package server

import (
	"errors"
	"net/http"
	"time"

	"cctv-sub/internal/model"
)

type subscriptionEpisodeRow struct {
	ID string `json:"id"`
	Number int `json:"number"`
	Title string `json:"title"`
	PageURL string `json:"pageUrl"`
	VideoGUID string `json:"videoGuid,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Status string `json:"status"`
	TaskID string `json:"taskId,omitempty"`
	FilePath string `json:"filePath,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	Speed string `json:"speed,omitempty"`
	Error string `json:"error,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
}

func (s *Server) subscriptionEpisodes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.store.Snapshot()
	found := false
	for _, sub := range state.Subscriptions {
		if sub.ID == id { found = true; break }
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("订阅不存在"))
		return
	}
	tasksByEpisode := make(map[string]model.DownloadTask)
	for _, task := range state.Downloads {
		if task.SubscriptionID != id { continue }
		for _, key := range []string{task.EpisodeID, task.VideoGUID, task.PageURL} {
			if key == "" { continue }
			current, ok := tasksByEpisode[key]
			if !ok || task.CreatedAt > current.CreatedAt { tasksByEpisode[key] = task }
		}
	}
	rows := make([]subscriptionEpisodeRow, 0)
	for _, episode := range state.Episodes {
		if episode.SubscriptionID != id { continue }
		row := subscriptionEpisodeRow{
			ID: episode.ID, Number: episode.Number, Title: episode.Title,
			PageURL: episode.PageURL, VideoGUID: episode.VideoGUID,
			PublishedAt: episode.PublishedAt, Status: "not_downloaded",
		}
		task, ok := tasksByEpisode[episode.ID]
		if !ok && episode.VideoGUID != "" { task, ok = tasksByEpisode[episode.VideoGUID] }
		if !ok && episode.PageURL != "" { task, ok = tasksByEpisode[episode.PageURL] }
		if ok {
			row.Status, row.TaskID = task.Status, task.ID
			row.FilePath, row.Progress, row.Speed, row.Error = task.FilePath, task.Progress, task.Speed, task.Error
			row.CreatedAt, row.CompletedAt = task.CreatedAt, task.CompletedAt
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"episodes": rows, "total": len(rows), "checkedAt": time.Now().Format(time.RFC3339)})
}
