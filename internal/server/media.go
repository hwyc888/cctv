package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

)

// completedVideoPath uses only persisted completed-task paths, never a path
// supplied by a request parameter. It is shared by playback and open-folder.
func (s *Server) completedVideoPath(id string) (string, error) {
	for _, task := range s.store.Snapshot().Downloads {
		if task.ID != id {
			continue
		}
		if task.Status != "completed" || task.FilePath == "" {
			return "", errors.New("视频尚未下载完成")
		}
		if !strings.EqualFold(filepath.Ext(task.FilePath), ".mp4") {
			return "", errors.New("下载文件不是 MP4")
		}
		return task.FilePath, nil
	}
	return "", errors.New("下载任务不存在")
}

func (s *Server) streamDownloadedVideo(w http.ResponseWriter, r *http.Request) {
	path, err := s.completedVideoPath(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("视频文件不存在: %w", err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, errors.New("视频文件不存在或不是普通文件"))
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func (s *Server) openDownloadedDirectory(w http.ResponseWriter, r *http.Request) {
	remoteHost, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	remoteIP := net.ParseIP(remoteHost)
	if splitErr != nil || remoteIP == nil || !remoteIP.IsLoopback() ||
		(r.Header.Get("Origin") != "" && !isAllowedOrigin(r.Header.Get("Origin"), r.Host)) ||
		r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, errors.New("只能在运行 CCTV 程序的电脑上打开目录；远程浏览器请通过共享文件夹访问"))
		return
	}
	path, err := s.completedVideoPath(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, errors.New("已下载的视频文件不存在"))
		return
	}
	if err := openLocalDirectory(path); err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func openLocalDirectory(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer.exe", "/select,", path)
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("当前 Debian/Linux 没有可用的图形桌面；请用 Samba 或文件共享访问保存目录")
		}
		command = exec.Command("xdg-open", filepath.Dir(path))
	default:
		return errors.New("当前操作系统暂不支持直接打开文件管理器")
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动文件管理器失败: %w", err)
	}
	return command.Process.Release()
}

