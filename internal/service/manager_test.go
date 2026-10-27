package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/store"
)

func TestPauseAllAndResumeAllPersistTaskState(t *testing.T) {
	dataDir := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(dataDir, "state.db"), dataDir, model.Settings{
		DownloadPath:        filepath.Join(dataDir, "downloads"),
		PollIntervalMinutes: 30,
		ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	if err := stateStore.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{
			{ID: "queued", Status: "queued", Progress: 8, Speed: "1 MB/s"},
			{ID: "active", Status: "downloading", Progress: 42, Speed: "2 MB/s"},
			{ID: "done", Status: "completed", Progress: 100},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(stateStore, cctv.New())
	if err := manager.PauseAll(); err != nil {
		t.Fatal(err)
	}
	paused := stateStore.Snapshot()
	if !paused.Settings.DownloadsPaused || paused.Downloads[0].Status != "paused" || paused.Downloads[1].Status != "paused" {
		t.Fatalf("queued and active tasks were not paused: %#v", paused)
	}
	if paused.Downloads[0].Progress != 0 || paused.Downloads[1].Progress != 0 || paused.Downloads[2].Status != "completed" {
		t.Fatalf("pause reset or changed the wrong tasks: %#v", paused.Downloads)
	}
	if err := manager.ResumeAll(); err != nil {
		t.Fatal(err)
	}
	resumed := stateStore.Snapshot()
	if resumed.Settings.DownloadsPaused || resumed.Downloads[0].Status != "queued" || resumed.Downloads[1].Status != "queued" {
		t.Fatalf("paused tasks were not resumed: %#v", resumed)
	}
}

func TestStorageCategoriesSupportHierarchyPathsAndSafeDeletion(t *testing.T) {
	dataDir := t.TempDir()
	stateStore, err := store.Open(context.Background(), filepath.Join(dataDir, "state.db"), dataDir, model.Settings{
		DownloadPath:        filepath.Join(dataDir, "default"),
		PollIntervalMinutes: 30,
		ConcurrentDownloads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	manager := NewManager(stateStore, cctv.New())
	defaultCategory := stateStore.Snapshot().Categories[0]
	parent, err := manager.AddCategory("儿童", defaultCategory.ID, filepath.Join(dataDir, "children"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := manager.AddCategory("动画", parent.ID, filepath.Join(dataDir, "animation"))
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != parent.ID {
		t.Fatalf("child category did not keep its parent: %#v", child)
	}
	if _, err := manager.UpdateCategory(parent.ID, parent.Name, child.ID, parent.DownloadPath); err == nil {
		t.Fatal("moving a category below its child should fail")
	}
	if err := stateStore.Update(func(state *model.State) error {
		state.Subscriptions = append(state.Subscriptions, model.Subscription{ID: "sub-1", StorageCategoryID: child.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteCategory(child.ID); err == nil {
		t.Fatal("a category used by a subscription should not be deleted")
	}
	if err := stateStore.Update(func(state *model.State) error {
		state.Subscriptions = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteCategory(parent.ID); err == nil {
		t.Fatal("a category with children should not be deleted")
	}
	if err := manager.DeleteCategory(child.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteCategory(parent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMediaPlaylistChoosesHighestResolutionThenBitrate(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=640x360\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=1920x1080\nhigh.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2200000,RESOLUTION=1920x1080\nbest.m3u8\n"))
	})
	mux.HandleFunc("/low.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nlow.ts\n"))
	})
	mux.HandleFunc("/high.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nhigh.ts\n"))
	})
	mux.HandleFunc("/best.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nbest0.ts\n#EXTINF:10,\nbest1.ts\n"))
	})

	playlist, err := loadMediaPlaylist(context.Background(), server.Client(), server.URL+"/master.m3u8", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Segments) != 2 || playlist.Segments[0].URL != server.URL+"/best0.ts" {
		t.Fatalf("unexpected playlist: %#v", playlist)
	}
}

func TestSanitizeFilename(t *testing.T) {
	got := sanitizeFilename(`《测试》/第1集: "开始"`)
	if got != `《测试》_第1集_ _开始_` {
		t.Fatalf("unexpected filename: %q", got)
	}
}

func TestSeriesDownloadDirCreatesSeriesLevelPath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "downloads")
	got := seriesDownloadDir(root, `《测试剧》/第一季`)
	want := filepath.Join(root, `《测试剧》_第一季`)
	if got != want {
		t.Fatalf("unexpected series directory: got %q want %q", got, want)
	}
	if got := seriesDownloadDir(root, "..."); got != filepath.Join(root, "未命名剧集") {
		t.Fatalf("empty series title should use fallback directory: %q", got)
	}
}

func TestDecryptCCTVPlayerEnvelope(t *testing.T) {
	clear := make([]byte, 188*6)
	for offset := 0; offset < len(clear); offset += 188 {
		clear[offset] = 0x47
		for i := offset + 1; i < offset+188; i++ {
			clear[i] = byte((i*29 + 17) % 256)
		}
	}
	key := bytes.Repeat([]byte{0x31, 0x72, 0xa4, 0x0f}, 8)
	iv := bytes.Repeat([]byte{0xe2, 0x19, 0x63, 0xb8}, 4)
	encrypted := encryptCCTVPlayerEnvelopeForTest(clear, key, iv)
	if !bytes.Equal(encrypted[:4], cctvPlayerMagic) {
		t.Fatal("test fixture is missing the player magic")
	}
	decoded, matched, err := decryptCCTVPlayerPayload(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !matched || !bytes.Equal(decoded, clear) {
		t.Fatal("player envelope did not round-trip")
	}
	if !looksLikeMediaSegment(decoded) {
		t.Fatal("decrypted TS was not recognized")
	}
	t.Logf("decrypted local CCTV envelope: encrypted=%d bytes clear=%d bytes magic=% X", len(encrypted), len(decoded), encrypted[:4])
}

func TestClearTSPassesThroughPlayerDecryptor(t *testing.T) {
	clear := make([]byte, 188*5)
	for offset := 0; offset < len(clear); offset += 188 {
		clear[offset] = 0x47
	}
	decoded, matched, err := decryptCCTVPlayerPayload(clear)
	if err != nil || matched || !bytes.Equal(decoded, clear) {
		t.Fatalf("clear TS should pass through: matched=%v err=%v", matched, err)
	}
}

func encryptCCTVPlayerEnvelopeForTest(clear, key, iv []byte) []byte {
	step := ((clamp(len(clear)/8, 2, 16) + len(clear)%7) % 16) + 1
	permutation := make([]int, len(clear))
	for i := range permutation {
		permutation[i] = i
	}
	for i := len(permutation) - 1; i > 0; i-- {
		position := (int(key[i%len(key)]) + int(iv[i%len(iv)]) + i) % (i + 1)
		permutation[position], permutation[i] = permutation[i], permutation[position]
	}
	encrypted := make([]byte, len(clear))
	for i := range encrypted {
		keyIndex := (i * step) % len(key)
		ivIndex := (i * (step + 1)) % len(iv)
		value := int(clear[permutation[i]])
		value ^= int(key[keyIndex])
		value ^= int(iv[ivIndex])
		value ^= i & 0xff
		value = (value + 3*int(key[keyIndex])) % 256
		value ^= step
		encrypted[i] = byte(value)
	}
	transformedKey := make([]byte, len(key))
	transformedIV := make([]byte, len(iv))
	keySeed := 37*len(key) + 43*len(iv)
	ivSeed := 53*len(key) + 61*len(iv)
	for i := range key {
		transformedKey[i] = key[i] ^ byte((keySeed+7*i)%256)
	}
	for i := range iv {
		transformedIV[i] = iv[i] ^ byte((ivSeed+11*i)%256)
	}
	keyOffset := 6 + len(encrypted)/2
	result := make([]byte, 6+len(encrypted)+len(key)+len(iv))
	copy(result[:4], cctvPlayerMagic)
	result[4], result[5] = byte(len(key)), byte(len(iv))
	copy(result[6:keyOffset], encrypted[:len(encrypted)/2])
	copy(result[keyOffset:keyOffset+len(key)], transformedKey)
	copy(result[keyOffset+len(key):len(result)-len(iv)], encrypted[len(encrypted)/2:])
	copy(result[len(result)-len(iv):], transformedIV)
	return result
}

func TestCCTVPlayerStreamIntegration(t *testing.T) {
	if os.Getenv("CCTV_INTEGRATION") != "1" {
		t.Skip("set CCTV_INTEGRATION=1 to test the live player stream")
	}
	const playerURL = "https://dh5wswx02.v.cntv.cn/asp/h5e/hls/main/0303000a/3/default/718c4a6804204fd195a05ee99ac7d36f/main.m3u8?maxbr=2048&contentid=15120519184043"
	playlist, err := loadMediaPlaylist(context.Background(), http.DefaultClient, playerURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Segments) == 0 {
		t.Fatal("live player playlist has no segments")
	}
	if !strings.Contains(playlist.Segments[0].URL, "/2000/") {
		t.Fatalf("highest player rendition was not selected: %s", playlist.Segments[0].URL)
	}
	data, err := fetchBytes(context.Background(), http.DefaultClient, playlist.Segments[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	clear, matched, err := decryptCCTVPlayerPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if !looksLikeMediaSegment(clear) {
		t.Fatal("live player segment is not valid after decryption")
	}
	t.Logf("validated one real CCTV player segment: wrapped=%v input=%d bytes clear=%d bytes", matched, len(data), len(clear))
}

func TestCCTVStreamCandidatesDecodeIntegration(t *testing.T) {
	if os.Getenv("CCTV_INTEGRATION") != "1" {
		t.Skip("set CCTV_INTEGRATION=1 to test live stream candidates")
	}
	ffmpegPath := os.Getenv("CCTV_FFMPEG_PATH")
	if ffmpegPath == "" {
		detectedPath, lookErr := exec.LookPath("ffmpeg")
		if lookErr != nil {
			t.Skip("ffmpeg is required")
		}
		ffmpegPath = detectedPath
	}
	ctx := context.Background()
	client := cctv.New()
	video, err := client.ResolveVideo(ctx, "https://tv.cctv.com/2026/06/30/VIDE6WX43sYVzv0Nw0srYLII260630.shtml")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved live stream candidates: %q", video.HLSURLs)
	for streamIndex, playerURL := range video.HLSURLs {
		playlist, loadErr := loadMediaPlaylist(ctx, client.HTTPClient(), playerURL, 0)
		if loadErr != nil || len(playlist.Segments) == 0 {
			t.Logf("stream %d playlist failed: %v", streamIndex+1, loadErr)
			continue
		}
		var sample bytes.Buffer
		limit := len(playlist.Segments)
		if limit > 2 {
			limit = 2
		}
		valid := true
		for i := 0; i < limit; i++ {
			segment := playlist.Segments[i]
			data, fetchErr := fetchBytes(ctx, client.HTTPClient(), segment.URL)
			if fetchErr != nil {
				valid = false
				break
			}
			if segment.KeyURL != "" {
				key, keyErr := fetchBytes(ctx, client.HTTPClient(), segment.KeyURL)
				if keyErr != nil {
					valid = false
					break
				}
				data, err = decryptAES128(data, key, segment.IV)
				if err != nil {
					valid = false
					break
				}
			}
			data, _, err = decryptCCTVPlayerPayload(data)
			if err != nil {
				valid = false
				break
			}
			_, _ = sample.Write(data)
		}
		if !valid {
			continue
		}
		path := filepath.Join(t.TempDir(), fmt.Sprintf("stream-%d.ts", streamIndex+1))
		if err := os.WriteFile(path, sample.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		probeResult := ""
		if ffprobePath := os.Getenv("CCTV_FFPROBE_PATH"); ffprobePath != "" {
			probe := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", path)
			probeOutput, _ := probe.CombinedOutput()
			probeResult = strings.TrimSpace(string(probeOutput))
		}
		cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-v", "error", "-i", path, "-map", "0:v:0", "-frames:v", "30", "-f", "null", "-")
		output, decodeErr := cmd.CombinedOutput()
		t.Logf("stream %d format=%q decode error=%v diagnostics=%q", streamIndex+1, probeResult, decodeErr, strings.TrimSpace(string(output)))
	}
}

func TestCCTVOfficialPlayerFullEpisodeIntegration(t *testing.T) {
	if os.Getenv("CCTV_INTEGRATION") != "1" {
		t.Skip("set CCTV_INTEGRATION=1 to decrypt and validate a complete live episode")
	}
	ffmpegPath := os.Getenv("CCTV_FFMPEG_PATH")
	ffprobePath := os.Getenv("CCTV_FFPROBE_PATH")
	if ffmpegPath == "" {
		t.Skip("CCTV_FFMPEG_PATH is required")
	}
	pageURL := os.Getenv("CCTV_FULL_EPISODE_URL")
	if pageURL == "" {
		pageURL = "https://tv.cctv.com/2026/06/25/VIDAmwELBZI07S5e81sF44XE260625.shtml"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	client := cctv.New()
	series, err := client.ResolveSeries(ctx, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Episodes) == 0 {
		t.Fatal("series page has no episodes")
	}
	video, err := client.ResolveVideo(ctx, series.Episodes[0].PageURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(video.HLSURLs) == 0 {
		t.Fatal("episode has no HLS candidates")
	}
	t.Logf("resolved HLS candidates: %q", video.HLSURLs)
	dataDir := t.TempDir()
	stateStore, err := store.Open(ctx, filepath.Join(dataDir, "state.db"), dataDir, model.Settings{
		DownloadPath:        "/tmp",
		PollIntervalMinutes: 30,
		ConcurrentDownloads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	manager := NewManager(stateStore, client)
	outputDirectory, err := os.MkdirTemp("/tmp", "cctv-full-episode-")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CCTV_KEEP_INTEGRATION_OUTPUT") != "1" {
		defer os.RemoveAll(outputDirectory)
	}
	output := filepath.Join(outputDirectory, "episode.mp4")
	if err := manager.downloadHLS(ctx, "integration", video.HLSURLs, output); err != nil {
		t.Fatal(err)
	}
	format := "highest playlist rendition"
	if ffprobePath != "" {
		probe := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", output)
		probeOutput, probeErr := probe.CombinedOutput()
		if probeErr != nil {
			t.Fatalf("ffprobe failed: %v: %s", probeErr, probeOutput)
		}
		format = strings.TrimSpace(string(probeOutput))
		if !strings.Contains(format, "1280,720") {
			t.Fatalf("highest 720P rendition was not downloaded: %q", format)
		}
	}
	t.Logf("decrypt output ready for full decode: series=%q episode=%q format=%s output=%s", series.Title, series.Episodes[0].Title, format, output)
	decode := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-v", "error", "-xerror", "-i", output, "-map", "0:v:0", "-f", "null", "-")
	decodeOutput, err := decode.CombinedOutput()
	if err != nil || len(bytes.TrimSpace(decodeOutput)) > 0 {
		t.Fatalf("full episode has decoding corruption: %v: %s", err, decodeOutput)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("official CCTV player decrypted a clean complete episode from %q: format=%s size=%d output=%s", series.Title, format, info.Size(), output)
}
