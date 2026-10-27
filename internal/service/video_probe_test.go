package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseFrameProbeRejectsMetadataOnlyAndAcceptsVideo(t *testing.T) {
	if _, err := parseFrameProbe([]byte("#dimensions 0: 1280x720\n#stream data\n")); err == nil {
		t.Fatal("a track with only metadata must not be reported as playable")
	}
	details, err := parseFrameProbe([]byte("#dimensions 0: 480x270\n#stream#, dts, pts, duration, size, hash\n0, 0, 0, 1, 194400, 0123456789abcdef\n"))
	if err != nil || details != "480×270" {
		t.Fatalf("valid decoded frame has incorrect dimensions: %q %v", details, err)
	}
}

func TestProbeVideoFramesReadsStreamAndChecksDecodedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script-based FFmpeg stub is for Linux CI")
	}
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ninput=$(cat)\ncase \"$input\" in *PLAYABLE*) printf '#dimensions 0: 480x270\\n0, 0, 0, 1, 1, hash\\n';; *) echo corrupt >&2; exit 2;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	quality, err := probeVideoFrames(context.Background(), bin, []byte("PLAYABLE"), "")
	if err != nil || quality != "480×270" {
		t.Fatalf("expected playable 480p stream, got %q %v", quality, err)
	}
	if _, err := probeVideoFrames(context.Background(), bin, bytes.Repeat([]byte{'X'}, 12), ""); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt video was not rejected: %v", err)
	}
}
