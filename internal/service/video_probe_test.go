package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
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

func TestChoosePlayableHLSFallsBackToDecodableSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell FFmpeg stub is for Linux CI")
	}
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ninput=$(cat)\ncase \"$input\" in *PLAYABLE*) printf '#dimensions 0: 480x270\\n0, 0, 0, 1, 1, hash\\n';; *) exit 2;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCTV_FFMPEG_PATH", bin)
	invalidWASM := filepath.Join(t.TempDir(), "invalid-worker.js")
	if err := os.WriteFile(invalidWASM, []byte("broken official player module"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCTV_WASM_SOURCE_PATH", invalidWASM)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:10,\nbad.ts\n")
		case "/good.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:10,\ngood.ts\n")
		case "/bad.ts":
			fmt.Fprint(w, "ENCRYPTED")
		case "/good.ts":
			fmt.Fprint(w, "PLAYABLE")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := NewManager(nil, cctv.New())
	selected, err := manager.choosePlayableHLS(context.Background(), []string{server.URL + "/bad.m3u8", server.URL + "/good.m3u8"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.useWASM || selected.quality != "480×270" || len(selected.playlist.Segments) != 1 || !strings.HasSuffix(selected.playlist.Segments[0].URL, "/good.ts") {
		t.Fatalf("did not fall back to clear usable stream: %#v", selected)
	}
}
