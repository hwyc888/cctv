package ffmpeg

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testFFmpegFixture(t *testing.T) string {
	t.Helper()
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("dummy test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindAdjacentExecutable(t *testing.T) {
	p := testFFmpegFixture(t)
	found, err := findAdjacent(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(found) != filepath.Clean(p) {
		t.Fatalf("found %q instead of %q", found, p)
	}
}

func TestFindHonorsExplicitPath(t *testing.T) {
	p := testFFmpegFixture(t)
	t.Setenv("CCTV_FFMPEG_PATH", p)
	found, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(found) != filepath.Clean(p) {
		t.Fatalf("found %q instead of configured %q", found, p)
	}
}

func TestFindRejectsInvalidExplicitPath(t *testing.T) {
	t.Setenv("CCTV_FFMPEG_PATH", filepath.Join(t.TempDir(), "missing-ffmpeg"))
	_, err := Find()
	if err == nil || !strings.Contains(err.Error(), "CCTV_FFMPEG_PATH") {
		t.Fatalf("expected an invalid explicit path error, got %v", err)
	}
}

func TestFindAdjacentMissing(t *testing.T) {
	_, err := findAdjacent(t.TempDir())
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}
