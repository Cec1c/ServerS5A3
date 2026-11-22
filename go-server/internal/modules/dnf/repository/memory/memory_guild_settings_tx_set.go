package memory

import (
	"context"

	"longheng.io/server/internal/modules/dnf/repository"
)

// memoryGuildSettingsTxSet 在内存事务中管理预加载的公会设置 scope 以及按需延迟快照的
// 受控公会历史归档 scope（guild-archive:v1:*），保持与 MySQL 单事务一致的原子提交与回滚。
type memoryGuildSettingsTxSet struct {
	base         repository.SettingsRepository
	allowArchive bool
	txs          map[string]*memorySettingsTransaction
	before       map[string]repository.SettingsRecord
	existed      map[string]bool
	order        []string
}

func newMemoryGuildSettingsTxSet(base repository.SettingsRepository, allowArchive bool) *memoryGuildSettingsTxSet {
	return &memoryGuildSettingsTxSet{
		base:         base,
		allowArchive: allowArchive,
		txs:          make(map[string]*memorySettingsTransaction, 2),
		before:       make(map[string]repository.SettingsRecord, 2),
		existed:      make(map[string]bool, 2),
		order:        make([]string, 0, 2),
	}
}

func (s *memoryGuildSettingsTxSet) preload(ctx context.Context, scope string) error {
	if _, ok := s.txs[scope]; ok {
		return nil
	}
	record, found, err := s.base.Load(ctx, scope)
	if err != nil {
		return err
	}
	s.txs[scope] = &memorySettingsTransaction{
		scope:  scope,
		record: repository.CloneSettings(record),
		exists: found,
	}
	s.before[scope] = record
	s.existed[scope] = found
	s.order = append(s.order, scope)
	return nil
}

func (s *memoryGuildSettingsTxSet) ensure(ctx context.Context, scope string) (*memorySettingsTransaction, error) {
	if tx := s.txs[scope]; tx != nil {
		return tx, nil
	}
	if !s.allowArchive || !repository.ValidGuildArchiveScope(scope) {
		return nil, repository.ErrGuildTransaction
	}
	if err := s.preload(ctx, scope); err != nil {
		return nil, err
	}
	return s.txs[scope], nil
}

func (s *memoryGuildSettingsTxSet) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	tx, err := s.ensure(ctx, scope)
	if err != nil {
		return repository.SettingsRecord{}, false, err
	}
	return tx.Load(ctx, scope)
}

func (s *memoryGuildSettingsTxSet) Save(ctx context.Context, record repository.SettingsRecord) error {
	tx, err := s.ensure(ctx, record.Scope)
	if err != nil {
		return err
	}
	return tx.Save(ctx, record)
}
