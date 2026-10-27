package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/cctvwasm"
)

// Run on a CI runner with CCTV_VISUAL_INTEGRATION=1 to validate real video
// decoding, not just the WASM module's initialization.
func TestLiveVideoVisualDecode(t *testing.T) {
	if os.Getenv("CCTV_VISUAL_INTEGRATION") != "1" {
		t.Skip("set CCTV_VISUAL_INTEGRATION=1 to test a real CCTV video segment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	url := os.Getenv("CCTV_VIDEO_TEST_URL")
	if url == "" {
		url = "https://tv.cctv.com/2026/06/30/VIDE6WX43sYVzv0Nw0srYLII260630.shtml"
	}
	client := cctv.New()
	video, err := client.ResolveVideo(ctx, url)
	if err != nil {
		t.Fatalf("resolve test video: %v", err)
	}
	t.Logf("selected video has %d HLS candidates", len(video.HLSURLs))
	var playlist mediaPlaylist
	var playlistErr error
	for _, uri := range video.HLSURLs {
		playlist, playlistErr = loadMediaPlaylist(ctx, client.HTTPClient(), uri, 0)
		if playlistErr == nil && len(playlist.Segments) > 0 {
			break
		}
	}
	if playlistErr != nil || len(playlist.Segments) == 0 {
		t.Fatalf("no accessible media playlist: %v", playlistErr)
	}
	t.Logf("playlist contains %d segments", len(playlist.Segments))
	engineSource, err := fetchBytes(ctx, client.HTTPClient(), cctvWorkerURL)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := cctvwasm.NewEngine(ctx, engineSource)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.Background())
	dir := os.Getenv("CCTV_VISUAL_OUTPUT")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var joined bytes.Buffer
	for i := 0; i < len(playlist.Segments) && i < 3; i++ {
		seg := playlist.Segments[i]
		raw, fetchErr := fetchBytes(ctx, client.HTTPClient(), seg.URL)
		if fetchErr != nil {
			t.Fatalf("fetch TS %d: %v", i, fetchErr)
		}
		if seg.KeyURL != "" {
			key, keyErr := fetchBytes(ctx, client.HTTPClient(), seg.KeyURL)
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			raw, err = decryptAES128(raw, key, seg.IV)
			if err != nil {
				t.Fatal(err)
			}
		}
		marker := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), i)
		clear, decryptErr := engine.DecryptMPEGTS(ctx, raw, marker, cctvPlayerOrigin)
		if decryptErr != nil {
			t.Fatalf("decrypt segment %d: %v", i, decryptErr)
		}
		t.Logf("segment=%d input=%d output=%d", i, len(raw), len(clear))
		joined.Write(clear)
	}
	path := filepath.Join(dir, "sample.ts")
	if err := os.WriteFile(path, joined.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height", "-of", "default=noprint_wrappers=1", path)
	probe, err := cmd.CombinedOutput()
	t.Logf("ffprobe: %v %s", err, strings.TrimSpace(string(probe)))
	if err != nil {
		t.Fatal(err)
	}
	frame := filepath.Join(dir, "first-frame.png")
	cmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-v", "error", "-xerror", "-y", "-i", path, "-map", "0:v:0", "-frames:v", "1", frame)
	render, err := cmd.CombinedOutput()
	t.Logf("ffmpeg first-frame exit=%v stderr=%s", err, strings.TrimSpace(string(render)))
	if err != nil {
		t.Fatalf("video cannot be decoded: %v", err)
	}
	cmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-v", "error", "-xerror", "-i", path, "-map", "0:v:0", "-f", "null", "-")
	decoded, err := cmd.CombinedOutput()
	t.Logf("ffmpeg full sample decode exit=%v stderr=%s", err, strings.TrimSpace(string(decoded)))
	if err != nil || len(bytes.TrimSpace(decoded)) > 0 {
		t.Fatalf("CCTV video decode is corrupt: %v %s", err, decoded)
	}
}
