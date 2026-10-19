package memory

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

func (s *memoryCharStore) LastDeletedProfession(ctx context.Context, accountID string, job, grow byte) (repository.CharacterRecord, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	var latest repository.CharacterRecord
	found := false
	for _, record := range s.Snapshot(nil) {
		if record.AccountID == accountID && record.Job == strconv.Itoa(int(job)) && byte(record.Stats["grow_type"])&15 == grow && characterDeleteFlag(record) != 0 &&
			(!found || record.UpdatedAt.After(latest.UpdatedAt)) {
			latest, found = repository.CloneCharacter(record), true
		}
	}
	return latest, found, nil
}

// 创建事务持有角色名称协调锁；在同一资格检查中转发删除历史读取。
func (s *memoryCharacterCreationStore) LastDeletedProfession(ctx context.Context, accountID string, job, grow byte) (repository.CharacterRecord, bool, error) {
	h, ok := s.base.(repository.CharacterDeletedProfessionReader)
	if !ok {
		return repository.CharacterRecord{}, false, fmt.Errorf("创建事务的删除历史不可用")
	}
	return h.LastDeletedProfession(ctx, accountID, job, grow)
}
