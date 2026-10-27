package service

import (
	"errors"
	"fmt"
	"os"
)

// replaceDownloadedFile leaves the old video intact while a replacement is
// downloading and validates successfully, then swaps it with the new file.
// Windows may reject an os.Rename to an existing destination, so keep a
// short-lived backup during the swap.
func replaceDownloadedFile(temporaryPath, finalPath string) error {
	info, err := os.Stat(finalPath)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(temporaryPath, finalPath)
	}
	if err != nil {
		return fmt.Errorf("检查已有视频文件: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("目标视频路径不是普通文件")
	}
	backupPath := finalPath + "." + newID("backup")
	if err := os.Rename(finalPath, backupPath); err != nil {
		return fmt.Errorf("备份原视频失败: %w", err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		if restoreErr := os.Rename(backupPath, finalPath); restoreErr != nil {
			return fmt.Errorf("保存新视频失败: %w；恢复旧文件也失败: %v（备份路径: %s）", err, restoreErr, backupPath)
		}
		return fmt.Errorf("保存新视频失败，已恢复旧文件: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("已保存新视频，但删除备份失败（%s）: %w", backupPath, err)
	}
	return nil
}
