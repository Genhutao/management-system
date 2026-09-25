package controller

import (
	"sync"
	"time"
)

// 登录失败限流：按 "账号|IP" 计数，连续失败达阈值后锁定一段时间。
// 进程内实现，重启即清零；多实例部署时需改为共享存储。
const (
	loginMaxFails     = 5
	loginLockDuration = 15 * time.Minute
)

type failRecord struct {
	fails     int
	lockedUntil time.Time
}

var loginGuard = struct {
	sync.Mutex
	records map[string]*failRecord
}{records: make(map[string]*failRecord)}

// loginAllowed 判断该账号+IP 是否被锁定；返回剩余锁定秒数。
func loginAllowed(key string) (bool, int) {
	loginGuard.Lock()
	defer loginGuard.Unlock()

	rec, ok := loginGuard.records[key]
	if !ok {
		return true, 0
	}
	if rec.lockedUntil.After(time.Now()) {
		return false, int(time.Until(rec.lockedUntil).Seconds()) + 1
	}
	return true, 0
}

func loginRecordFailure(key string) {
	loginGuard.Lock()
	defer loginGuard.Unlock()

	rec, ok := loginGuard.records[key]
	if !ok {
		rec = &failRecord{}
		loginGuard.records[key] = rec
	}
	rec.fails++
	if rec.fails >= loginMaxFails {
		rec.lockedUntil = time.Now().Add(loginLockDuration)
		rec.fails = 0
	}
}

func loginRecordSuccess(key string) {
	loginGuard.Lock()
	defer loginGuard.Unlock()
	delete(loginGuard.records, key)
}
