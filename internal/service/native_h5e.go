package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// findNativeH5E locates an optional GPL-3.0 licensed standalone decoder.
// It is shipped as a separate executable, not linked to the Go binary.
func findNativeH5E() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CCTV_H5E_DECRYPT_PATH")); configured != "" {
		tool, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("CCTV_H5E_DECRYPT_PATH=%q: %w", configured, err)
		}
		return tool, nil
	}
	name := "cctv-h5e-decrypt"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		if tool, err := exec.LookPath(filepath.Join(filepath.Dir(self), name)); err == nil {
			return tool, nil
		}
	}
	if runtime.GOOS == "linux" {
		if tool, err := exec.LookPath("/usr/lib/cctv-sub/cctv-h5e-decrypt"); err == nil {
			return tool, nil
		}
	}
	return exec.LookPath(name)
}

// nativeH5EDecryptTS executes the upstream native H5E decoder on one
// already downloaded and (if applicable) AES-decrypted MPEG-TS segment.
// The upstream CLI operates on files; temporary files are private and
// removed on success, failure, or cancellation.
func nativeH5EDecryptTS(ctx context.Context, executable string, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input) == 0 || len(input)%188 != 0 {
		return nil, errors.New("央视 H5E 输入不是完整 MPEG-TS 分片")
	}
	directory, err := os.MkdirTemp("", "cctv-native-h5e-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)

	source := filepath.Join(directory, "input.ts")
	target := filepath.Join(directory, "output.ts")
	if err := os.WriteFile(source, input, 0o600); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, source, target)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 320 {
			message = message[:320]
		}
		return nil, fmt.Errorf("执行原生解密程序失败: %w (%s)", err, message)
	}
	clear, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("读取原生解密结果: %w", err)
	}
	if len(clear) != len(input) {
		return nil, fmt.Errorf("原生解密结果长度异常: %d != %d", len(clear), len(input))
	}
	return clear, nil
}
