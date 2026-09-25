package repository

import (
	"errors"
	"log"

	"xgh-system/internal/model"
)

// ErrDBUnavailable 数据库尚未初始化完成。
var ErrDBUnavailable = errors.New("数据库不可用")

// RecordOperation 追加一条操作留痕。留痕表只增不改，不提供任何更新与删除入口。
func RecordOperation(entry *model.OperationLog) error {
	if DB == nil {
		log.Printf("[Audit][DEGRADED] 数据库不可用，留痕丢失: action=%s operator=%d", entry.Action, entry.OperatorID)
		return ErrDBUnavailable
	}
	if err := DB.Create(entry).Error; err != nil {
		log.Printf("[Audit][DEGRADED] 留痕写入失败: action=%s operator=%d err=%v", entry.Action, entry.OperatorID, err)
		return err
	}
	return nil
}
