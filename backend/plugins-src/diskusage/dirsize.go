package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

// dirSize 统计一个目录下的文件数与字节数。
// 上限是防一个把上 GB 素材丢进 plugins/ 的插件把演示进程拖住——演示插件更该懂这条规矩。
func dirSize(root string) (int, int64, error) {
	var count int
	var bytes int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		count++
		if count > 5000 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", root, err)
	}
	return count, bytes, nil
}
