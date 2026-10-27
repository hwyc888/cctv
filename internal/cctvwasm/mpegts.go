package cctvwasm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bluenviron/mediacommon/pkg/formats/mpegts"
)

// DecryptMPEGTS decrypts a single CCTV MPEG-TS segment with a fresh official
// VOD session and rewrites it as a standards-compliant clear MPEG-TS stream.
func (e *Engine) DecryptMPEGTS(ctx context.Context, input []byte, mediaTagID, activeURL string) ([]byte, error) {
	reader, err := mpegts.NewReader(bytes.NewReader(input))
	if err != nil {
		return nil, fmt.Errorf("解析央视 TS 轨道: %w", err)
	}
	tracks := reader.Tracks()
	var videoTrack *mpegts.Track
	for _, track := range tracks {
		if _, ok := track.Codec.(*mpegts.CodecH264); ok {
			videoTrack = track
			break
		}
	}
	if videoTrack == nil {
		return nil, errors.New("央视 TS 中没有 H.264 视频轨道")
	}

	session, err := e.NewSession(ctx, mediaTagID, activeURL)
	if err != nil {
		return nil, err
	}
	defer session.Close(context.Background())

	var output bytes.Buffer
	writer := mpegts.NewWriter(&output, tracks)
	videoAccessUnits := 0
	decryptActive := false
	type pendingAccessUnit struct {
		pts, dts   int64
		accessUnit [][]byte
	}
	var pendingVideo *pendingAccessUnit
	videoFrameDuration := int64(3600)
	processVideo := func(track *mpegts.Track, pending *pendingAccessUnit, playerDTS int64) error {
		// mediacommon consumes the input access-unit delimiter (NAL type 9)
		// and the writer regenerates it. CCTV's worker updates its VOD state
		// for that delimiter before processing the remaining NAL units.
		if err := session.Update(); err != nil {
			return fmt.Errorf("更新央视访问单元状态: %w", err)
		}
		decrypted := make([][]byte, 0, len(pending.accessUnit))
		for _, nalu := range pending.accessUnit {
			if len(nalu) == 0 {
				continue
			}
			if err := session.Update(); err != nil {
				return err
			}
			naluType := nalu[0] & 0x1f
			if naluType == 25 {
				decryptActive = len(nalu) > 1 && nalu[1] == 1
				if _, err := session.DecryptNAL(mediaTagID, nalu); err != nil {
					return fmt.Errorf("解密央视控制 NAL（长度 %d，DTS %d）: %w", len(nalu), pending.dts, err)
				}
				continue
			}
			if decryptActive && (naluType == 1 || naluType == 5) {
				clearNALU, err := session.DecryptNAL(session.ArgumentForDTS(playerDTS), nalu)
				if err != nil {
					return fmt.Errorf("解密央视视频 NAL（类型 %d，长度 %d，播放器 DTS %d）: %w", naluType, len(nalu), playerDTS, err)
				}
				if len(clearNALU) == 0 {
					return errors.New("央视 WASM 返回了空视频 NAL")
				}
				nalu = clearNALU
			}
			decrypted = append(decrypted, nalu)
		}
		if len(decrypted) == 0 {
			return nil
		}
		videoAccessUnits++
		return writer.WriteH2642(track, pending.pts, pending.dts, decrypted)
	}
	var callbackErr error
	reader.OnDecodeError(func(err error) {
		if callbackErr == nil {
			callbackErr = err
		}
	})
	for _, track := range tracks {
		track := track
		switch track.Codec.(type) {
		case *mpegts.CodecH264:
			reader.OnDataH264(track, func(pts int64, dts int64, accessUnit [][]byte) error {
				if pendingVideo != nil {
					if duration := dts - pendingVideo.dts; duration > 0 && duration <= 90000 {
						videoFrameDuration = duration
					}
					if err := processVideo(track, pendingVideo, dts); err != nil {
						return err
					}
				}
				cloned := make([][]byte, len(accessUnit))
				for index, nalu := range accessUnit {
					cloned[index] = append([]byte(nil), nalu...)
				}
				pendingVideo = &pendingAccessUnit{pts: pts, dts: dts, accessUnit: cloned}
				return nil
			})
		case *mpegts.CodecMPEG4Audio:
			reader.OnDataMPEG4Audio(track, func(pts int64, accessUnits [][]byte) error {
				return writer.WriteMPEG4Audio(track, pts, accessUnits)
			})
		case *mpegts.CodecMPEG1Audio:
			reader.OnDataMPEG1Audio(track, func(pts int64, frames [][]byte) error {
				return writer.WriteMPEG1Audio(track, pts, frames)
			})
		default:
			return nil, fmt.Errorf("央视 TS 包含暂不支持的轨道 %T", track.Codec)
		}
	}

	for {
		err := reader.Read()
		if callbackErr != nil {
			return nil, fmt.Errorf("解析央视 TS: %w", callbackErr)
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "no more packets") || strings.Contains(err.Error(), "astits: reading") {
			break
		}
		return nil, fmt.Errorf("读取央视 TS: %w", err)
	}
	if pendingVideo != nil {
		if err := processVideo(videoTrack, pendingVideo, pendingVideo.dts+videoFrameDuration); err != nil {
			return nil, fmt.Errorf("完成央视 TS 最后一帧: %w", err)
		}
	}
	if videoAccessUnits == 0 {
		return nil, errors.New("央视 TS 没有输出视频帧")
	}
	return output.Bytes(), nil
}
