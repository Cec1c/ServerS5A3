package memory

import "context"

// 内存事务用补偿保存模拟数据库回滚，只有该私有上下文允许恢复未提交前快照。
type levelUpRollbackContextKey struct{}

func levelUpRollbackContext() context.Context {
	return context.WithValue(context.Background(), levelUpRollbackContextKey{}, true)
}
func isLevelUpRollback(ctx context.Context) bool {
	return ctx != nil && ctx.Value(levelUpRollbackContextKey{}) == true
}
