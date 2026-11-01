package repository

import (
	"context"
	"errors"

	"longheng.io/server/internal/platform/db"
)

// AccountRecordCreator把账号及初始元数据作为一次原子创建；已有账号零覆盖。
// 此能力独立于普通Save，禁止用Load后Save退化实现。
type AccountRecordCreator = AccountCreateIfAbsentWriter

var ErrAccountCreationUnavailable = errors.New("atomic account creation is unavailable")

func CreateAccountIfAbsent(ctx context.Context, store AccountRepository, record AccountRecord) (bool, error) {
	if AccountKey(record) == "" {
		return false, db.ErrRecordKeyRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	creator, ok := store.(AccountRecordCreator)
	if !ok {
		return false, ErrAccountCreationUnavailable
	}
	return creator.CreateIfAbsent(ctx, CloneAccount(record))
}
