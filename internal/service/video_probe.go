package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"cctv-sub/internal/ffmpeg"
)

type playableHLS struct {
	playlist  mediaPlaylist
	useWASM   bool
	useNative bool
	quality   string
}

var videoDimensionRE = regexp.MustCompile(`(?m)^#dimensions 0: ([0-9]+)x([0-9]+)`)

// choosePlayableHLS checks that actual H.264 frames can be decoded before
// committing to a full download, rather than assuming any 720P TS is clear.
func (m *Manager) choosePlayableHLS(ctx context.Context, playlistURLs []string) (playableHLS, error) {
	executable, err := ffmpeg.Find()
	if err != nil {
		return playableHLS{}, fmt.Errorf("未找到 FFmpeg，无法检测视频画面: %w", err)
	}
	nativeExecutable, nativeErr := findNativeH5E()
	failures := make([]string, 0, len(playlistURLs))
	for index, url := range playlistURLs {
		if err := ctx.Err(); err != nil {
			return playableHLS{}, err
		}
		playlist, err := loadMediaPlaylist(ctx, m.cctv.HTTPClient(), url, 0)
		if err != nil || len(playlist.Segments) == 0 {
			failures = append(failures, fmt.Sprintf("源%d: 播放列表不可用", index+1))
			continue
		}
		rawSegments, err := m.sampleSegments(ctx, playlist.Segments, 2)
		if err != nil {
			failures = append(failures, fmt.Sprintf("源%d: 获取样本失败 (%v)", index+1, err))
			continue
		}
		quality, err := probeVideoFrames(ctx, executable, bytes.Join(rawSegments, nil), "")
		if err == nil {
			return playableHLS{playlist: playlist, quality: quality}, nil
		}
		// A separate GPL-3.0 licensed native helper can handle modern H5E
		// NAL type 1/5/25 directly, without emulating CCTV's WASM host.
		if nativeErr == nil {
			var nativeSample bytes.Buffer
			var decodeErr error
			for _, raw := range rawSegments {
				var decrypted []byte
				decrypted, decodeErr = nativeH5EDecryptTS(ctx, nativeExecutable, raw)
				if decodeErr != nil {
					break
				}
				nativeSample.Write(decrypted)
			}
			if decodeErr == nil {
				quality, decodeErr = probeVideoFrames(ctx, executable, nativeSample.Bytes(), "")
				if decodeErr == nil {
					return playableHLS{playlist: playlist, useNative: true, quality: quality}, nil
				}
			}
		}

		engine, initErr := m.cctvDecryptorEngine(ctx)
		if initErr != nil {
			failures = append(failures, fmt.Sprintf("源%d: WASM 初始化失败 (%v)", index+1, initErr))
			continue
		}
		var decrypted bytes.Buffer
		decryptErr := error(nil)
		for i, raw := range rawSegments {
			mediaTagID := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), i)
			clear, err := engine.DecryptMPEGTS(ctx, raw, mediaTagID, cctvPlayerOrigin)
			if err != nil {
				decryptErr = err
				break
			}
			decrypted.Write(clear)
		}
		if decryptErr != nil {
			failures = append(failures, fmt.Sprintf("源%d: WASM 解密失败 (%v)", index+1, decryptErr))
			continue
		}
		quality, err = probeVideoFrames(ctx, executable, decrypted.Bytes(), "")
		if err == nil {
			return playableHLS{playlist: playlist, useWASM: true, quality: quality}, nil
		}
		failures = append(failures, fmt.Sprintf("源%d: 解密后的视频帧损坏", index+1))
	}
	if len(failures) == 0 {
		return playableHLS{}, errors.New("没有可用的 HLS 视频源")
	}
	return playableHLS{}, fmt.Errorf("所有候选源的视频均无法通过解码验证，已停止以免生成花屏 MP4: %s", strings.Join(failures, "; "))
}

func (m *Manager) sampleSegments(ctx context.Context, segments []hlsSegment, max int) ([][]byte, error) {
	keyCache := make(map[string][]byte)
	if len(segments) < max {
		max = len(segments)
	}
	result := make([][]byte, 0, max)
	for i := 0; i < max; i++ {
		segment := segments[i]
		data, err := fetchBytes(ctx, m.cctv.HTTPClient(), segment.URL)
		if err != nil {
			return nil, err
		}
		if segment.KeyURL != "" {
			key := keyCache[segment.KeyURL]
			if key == nil {
				key, err = fetchBytes(ctx, m.cctv.HTTPClient(), segment.KeyURL)
				if err != nil {
					return nil, err
				}
				keyCache[segment.KeyURL] = key
			}
			data, err = decryptAES128(data, key, segment.IV)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, data)
	}
	return result, nil
}

// probeVideoFrames verifies that FFmpeg actually emitted decoded frames;
// ffprobe metadata alone can succeed on an encrypted H.264 track.
func probeVideoFrames(ctx context.Context, executable string, sample []byte, filePath string) (string, error) {
	args := []string{"-hide_banner", "-v", "error", "-xerror"}
	if filePath == "" {
		args = append(args, "-f", "mpegts", "-i", "pipe:0")
	} else {
		args = append(args, "-i", filePath)
	}
	args = append(args, "-map", "0:v:0", "-frames:v", "3", "-f", "framemd5", "-")
	cmd := exec.CommandContext(ctx, executable, args...)
	if filePath == "" {
		cmd.Stdin = bytes.NewReader(sample)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		diagnostic := strings.TrimSpace(string(out))
		if len(diagnostic) > 240 {
			diagnostic = diagnostic[:240]
		}
		return "", fmt.Errorf("解码视频帧: %w (%s)", err, diagnostic)
	}
	return parseFrameProbe(out)
}

func parseFrameProbe(output []byte) (string, error) {
	videoFrames := 0
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "0,") {
			videoFrames++
		}
	}
	if videoFrames == 0 {
		return "", errors.New("FFmpeg 未输出可解码的视频帧")
	}
	match := videoDimensionRE.FindSubmatch(output)
	if len(match) != 3 {
		return "", errors.New("FFmpeg 未提供解码视频尺寸")
	}
	return string(match[1]) + "×" + string(match[2]), nil
}

func checkVideoFilePlayable(ctx context.Context, filename string) error {
	executable, err := ffmpeg.Find()
	if err != nil {
		return err
	}
	_, err = probeVideoFrames(ctx, executable, nil, filename)
	return err
}
