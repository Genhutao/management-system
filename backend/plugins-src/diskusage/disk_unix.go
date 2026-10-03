//go:build !windows

package main

import (
	"fmt"
	"syscall"
)

// diskUsage 返回 total/free GB 与已用百分比。
func diskUsage(path string) (totalGB, freeGB float64, usedPct float64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := float64(st.Bsize)
	total := float64(st.Blocks) * bs / (1024 * 1024 * 1024)
	free := float64(st.Bavail) * bs / (1024 * 1024 * 1024)
	if total <= 0 {
		return 0, 0, 0, fmt.Errorf("盘 %s 报回来的总容量是 0", path)
	}
	return total, free, (total - free) / total * 100, nil
}
