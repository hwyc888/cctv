package ffmpeg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Find is shared by the HTTP health check and the MP4 remuxer.
// Explicit configuration takes precedence over a bundled executable or PATH.
func Find() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CCTV_FFMPEG_PATH")); configured != "" {
		found, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("CCTV_FFMPEG_PATH=%q: %w", configured, err)
		}
		return found, nil
	}
	if executable, err := os.Executable(); err == nil {
		if found, err := findAdjacent(filepath.Dir(executable)); err == nil {
			return found, nil
		}
	}
	return exec.LookPath("ffmpeg")
}

func findAdjacent(directory string) (string, error) {
	names := []string{"ffmpeg"}
	if runtime.GOOS == "windows" {
		names = []string{"ffmpeg.exe", "ffmpeg"}
	}
	for _, name := range names {
		if found, err := exec.LookPath(filepath.Join(directory, name)); err == nil {
			return found, nil
		}
	}
	return "", exec.ErrNotFound
}
