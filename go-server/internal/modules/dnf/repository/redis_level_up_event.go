package repository

import "context"

// 恢复扫描直接委托权威仓储，不能从可重建的Redis角色列表推断历史奖励。
func (s *RedisCachedCharStore) ListPendingLevelUpCharacters(ctx context.Context, after string, limit int) ([]string, error) {
	if reader, ok := s.backing.(PendingLevelUpReader); ok {
		return reader.ListPendingLevelUpCharacters(ctx, after, limit)
	}
	return nil, ErrRepoMissing
}
