package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cctv-sub/internal/model"
)

type DeleteTasksResult struct {
	Deleted      int `json:"deleted"`
	FilesDeleted int `json:"filesDeleted"`
}

type stagedVideo struct {
	original string
	temporary string
}

// DeleteTasks permanently removes the selected completed files and task records.
// A live worker must first be cancelled and fully stopped, so it cannot write
// the same output again while it is being removed.
func (m *Manager) DeleteTasks(ids []string) (DeleteTasksResult, error) {
	var result DeleteTasksResult
	if len(ids) == 0 || len(ids) > 200 {
		return result, errors.New("请选择 1 到 200 个任务进行删除")
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || selected[id] {
			return result, errors.New("删除任务列表包含空 ID 或重复 ID")
		}
		selected[id] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var staged []stagedVideo
	err := m.store.Update(func(state *model.State) error {
		tasks := make([]model.DownloadTask, 0, len(ids))
		for _, task := range state.Downloads {
			if !selected[task.ID] {
				continue
			}
			if task.Status == "downloading" || m.active[task.ID] != nil {
				return fmt.Errorf("《%s》仍在下载，请先取消任务并等待停止后再删除", task.Title)
			}
			tasks = append(tasks, task)
		}
		if len(tasks) != len(selected) {
			return errors.New("有下载任务已不存在，请刷新列表后重试")
		}

		files := make(map[string]bool)
		for _, task := range tasks {
			if task.FilePath == "" {
				continue
			}
			filename, exists, err := safeTaskDeletePath(state, task)
			if err != nil {
				return fmt.Errorf("无法安全删除《%s》的视频文件: %w", task.Title, err)
			}
			if !exists || files[filename] {
				continue
			}
			for _, other := range state.Downloads {
				if !selected[other.ID] && other.FilePath != "" &&
					filepath.Clean(other.FilePath) == filename {
					return errors.New("同一视频文件还被其他任务使用，请一起选择这些任务后再删除")
				}
			}
			files[filename] = true
		}
		for filename := range files {
			temp := filepath.Join(filepath.Dir(filename), "."+filepath.Base(filename)+"."+newID("delete")+".pending")
			if err := os.Rename(filename, temp); err != nil {
				return fmt.Errorf("移动待删除文件失败（请先关闭正在播放的视频）: %w", err)
			}
			staged = append(staged, stagedVideo{original: filename, temporary: temp})
		}

		retained := make([]model.DownloadTask, 0, len(state.Downloads)-len(selected))
		affectedSubscriptions := make(map[string]bool)
		for _, task := range state.Downloads {
			if selected[task.ID] {
				if task.SubscriptionID != "" {
					affectedSubscriptions[task.SubscriptionID] = true
				}
				continue
			}
			retained = append(retained, task)
		}
		state.Downloads = retained
		for subID := range affectedSubscriptions {
			recountDownloaded(state, subID)
		}
		return nil
	})
	if err != nil {
		var restoreErr error
		for i := len(staged)-1; i >= 0; i-- {
			if rollbackErr := os.Rename(staged[i].temporary, staged[i].original); rollbackErr != nil {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("恢复 %s 失败: %w", staged[i].original, rollbackErr))
			}
		}
		return result, errors.Join(err, restoreErr)
	}
	result.Deleted = len(selected)
	for _, video := range staged {
		if err := os.Remove(video.temporary); err != nil {
			return result, fmt.Errorf("已删除任务记录，但无法彻底清除文件 %s: %w", video.temporary, err)
		}
		result.FilesDeleted++
	}
	m.Notify()
	return result, nil
}

// A task may only remove an existing regular MP4 underneath its own current
// configured category. Resolved parents prevent traversal via symlinked dirs.
func safeTaskDeletePath(state *model.State, task model.DownloadTask) (string, bool, error) {
	filename := filepath.Clean(task.FilePath)
	if !filepath.IsAbs(filename) || !strings.EqualFold(filepath.Ext(filename), ".mp4") {
		return "", false, errors.New("视频路径必须是完整的 MP4 文件路径")
	}
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return filename, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("视频文件不是普通文件，拒绝删除目录或符号链接")
	}
	categoryID := task.StorageCategoryID
	if task.SubscriptionID != "" {
		for _, sub := range state.Subscriptions {
			if sub.ID == task.SubscriptionID {
				categoryID = sub.StorageCategoryID
				break
			}
		}
	}
	var root string
	for _, category := range state.Categories {
		if category.ID == categoryID {
			root = category.DownloadPath
			break
		}
	}
	if root == "" {
		return "", false, errors.New("原保存分类已不存在，请先检查文件保存路径")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false, fmt.Errorf("保存分类路径不可用: %w", err)
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return "", false, err
	}
	relative, err := filepath.Rel(realRoot, realParent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false, errors.New("文件不在当前分类的保存目录内，已拒绝删除")
	}
	if relative == "." {
		return "", false, errors.New("视频不能直接位于下载分类根目录")
	}
	return filename, true, nil
}
