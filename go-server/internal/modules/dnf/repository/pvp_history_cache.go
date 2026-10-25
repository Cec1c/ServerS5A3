package repository

import (
	"context"
	"fmt"
)

// 缓存包装不能隐藏权威仓储的职业删除历史，读取仍直达底层。
func (s *RedisCachedCharStore) LastDeletedProfession(ctx context.Context, accountID string, job, grow byte) (CharacterRecord, bool, error) {
	if s != nil {
		if history, ok := s.backing.(CharacterDeletedProfessionReader); ok {
			return history.LastDeletedProfession(ctx, accountID, job, grow)
		}
	}
	return CharacterRecord{}, false, fmt.Errorf("决斗角色删除历史读取不可用")
}
