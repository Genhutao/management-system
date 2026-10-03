//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// diskUsage 走 kernel32!GetDiskFreeSpaceExW。
//
// 为什么不但心用 golang.org/x/sys/windows：那个包确实已经在依赖图里（indirect），
// 但演示插件对插件作者立的规矩是"标准库就够写一个插件"，
// 为了省八行代码把自己破例一次，这条规矩就再也没人信了。
// go.mod 因此一个字都不动，交叉编译机上也多一个包都不用下。
const gib = 1024.0 * 1024 * 1024

var (
	modkernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = modkernel32.NewProc("GetDiskFreeSpaceExW")
)

func diskUsage(path string) (totalGB, freeGB float64, usedPct float64, err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, err
	}
	var freeAvail, totalBytes, totalFree uint64
	r, _, e := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		// LazyProc 的 err 在成功时是 nil，但个别路径会回一个 Errno(0)：两种都当成"没细节可给"
		if e != nil && e != syscall.Errno(0) {
			return 0, 0, 0, fmt.Errorf("GetDiskFreeSpaceExW(%s): %w", path, e)
		}
		return 0, 0, 0, fmt.Errorf("GetDiskFreeSpaceExW(%s) 返回失败", path)
	}
	if totalBytes == 0 {
		return 0, 0, 0, fmt.Errorf("盘 %s 报回来的总容量是 0", path)
	}
	total := float64(totalBytes) / gib
	free := float64(totalFree) / gib
	return total, free, (total - free) / total * 100, nil
}
