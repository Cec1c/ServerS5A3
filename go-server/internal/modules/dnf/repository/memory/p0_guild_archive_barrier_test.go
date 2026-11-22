package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

type barrierMemorySettingsStore struct {
	base               *memorySettingsStore
	targetArchiveScope string
	onArchiveSaved     func()
	onArchiveDeleted   func()
	onRegistryRestored func()
	archiveSavedOnce   atomic.Bool
}

func (s *barrierMemorySettingsStore) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	return s.base.Load(ctx, scope)
}

func (s *barrierMemorySettingsStore) Save(ctx context.Context, record repository.SettingsRecord) error {
	if err := s.base.Save(ctx, record); err != nil {
		return err
	}
	if record.Scope == s.targetArchiveScope && s.archiveSavedOnce.CompareAndSwap(false, true) && s.onArchiveSaved != nil {
		s.onArchiveSaved()
	} else if record.Scope == repository.GuildRegistryScope && s.archiveSavedOnce.Load() && s.onRegistryRestored != nil {
		s.onRegistryRestored()
	}
	return nil
}

func (s *barrierMemorySettingsStore) Delete(ctx context.Context, scope string) error {
	if err := s.base.Delete(ctx, scope); err != nil {
		return err
	}
	if scope == s.targetArchiveScope && s.onArchiveDeleted != nil {
		s.onArchiveDeleted()
	}
	return nil
}

// TestP0MemoryMidCommitPhysicalArchiveRollbackBarrier 确定性验证：
// 事务 B 在物理提交循环中已经把新建归档键真实写入底层 SettingsStore 后因 ctx 取消触发回滚；
// 并发归档事务 A 在此精确时刻发起 WithinGuildArchive，必须严格等到 B 完成底层回滚并释放 sharedMu
// 之后才能进入回调，绝不会读到 B 已写出但终将被删除的临时归档，最终有效归档完整保留。
func TestP0MemoryMidCommitPhysicalArchiveRollbackBarrier(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	owner := repository.GuildOwner{AccountID: "a1", CharacterID: "1"}
	receiptScope := repository.GuildReceiptArchiveScope(fmt.Sprintf("%064x", 0x909))

	baseGroup := NewMemoryGroup()
	baseSettings := baseGroup.Settings.(*memorySettingsStore)
	baseChars := baseGroup.Character
	if err := baseChars.Save(ctx, repository.CharacterRecord{CharacterID: owner.CharacterID, AccountID: owner.AccountID, Name: "c1"}); err != nil {
		t.Fatal(err)
	}
	r0 := repository.SettingsRecord{
		Scope:     repository.GuildRegistryScope,
		Values:    map[string]string{"state": "R0-with-receipt"},
		UpdatedAt: now,
	}
	if err := baseSettings.Save(ctx, r0); err != nil {
		t.Fatal(err)
	}

	bArchivePhysicallyWritten := make(chan struct{})
	aReadyToEnterArchive := make(chan struct{})
	var bArchiveDeleted atomic.Bool
	var bRegistryRestored atomic.Bool
	bCtx, cancelB := context.WithCancel(ctx)

	wrappedSettings := &barrierMemorySettingsStore{
		base:               baseSettings,
		targetArchiveScope: receiptScope,
		onArchiveSaved: func() {
			// 此刻 B 已将 R1 主表和 receiptScope 归档都物理写入了底层存储。
			close(bArchivePhysicallyWritten)
			// 等到测试协程启动 A 准备争抢锁后，再取消 B 的上下文触发物理回滚（零时间休眠假设）。
			<-aReadyToEnterArchive
			cancelB()
		},
		onArchiveDeleted: func() {
			bArchiveDeleted.Store(true)
		},
		onRegistryRestored: func() {
			bRegistryRestored.Store(true)
		},
	}

	var sharedMu sync.Mutex
	uow := &memoryGuildUnitOfWork{
		sharedMu:  &sharedMu,
		character: baseChars,
		settings:  wrappedSettings,
	}

	bDone := make(chan error, 1)
	go func() {
		bDone <- uow.WithinGuild(bCtx, []repository.GuildOwner{owner}, func(tx repository.Group) error {
			if err := tx.Settings.Save(bCtx, repository.SettingsRecord{
				Scope:     repository.GuildRegistryScope,
				Values:    map[string]string{"state": "R1-without-receipt-from-B"},
				UpdatedAt: now.Add(time.Second),
			}); err != nil {
				return err
			}
			return tx.Settings.Save(bCtx, repository.SettingsRecord{
				Scope:     receiptScope,
				Values:    map[string]string{"receipt": "temp-from-B"},
				UpdatedAt: now.Add(time.Second),
			})
		})
	}()

	// 等到 B 已经把临时归档物理落盘，但在回滚前阻塞。
	<-bArchivePhysicallyWritten
	// 确认此刻底层存储里确实已经存在 B 写出的临时归档（覆盖真实“物理已写→随后回滚”交错）。
	if _, found, err := baseSettings.Load(ctx, receiptScope); err != nil || !found {
		t.Fatalf("expected B's uncommitted archive physically written before rollback, found=%v err=%v", found, err)
	}

	aDone := make(chan error, 1)
	go func() {
		close(aReadyToEnterArchive)
		aDone <- uow.WithinGuildArchive(ctx, func(txSettings repository.SettingsRepository) error {
			// A 进入事务时，B 的物理回滚（删除临时归档 + 恢复 R0）必须已经全部完成。
			if !bArchiveDeleted.Load() || !bRegistryRestored.Load() {
				return fmt.Errorf("WithinGuildArchive entered before B finished rollback: deleted=%v restored=%v", bArchiveDeleted.Load(), bRegistryRestored.Load())
			}
			// A 绝不能看到 B 已回滚的临时归档，且必须看到还原后的 R0。
			if _, found, err := txSettings.Load(ctx, receiptScope); err != nil || found {
				return fmt.Errorf("A observed rolled-back archive: found=%v err=%v", found, err)
			}
			regRec, found, err := txSettings.Load(ctx, repository.GuildRegistryScope)
			if err != nil || !found || regRec.Values["state"] != "R0-with-receipt" {
				return fmt.Errorf("A expected restored R0, got found=%v val=%q err=%v", found, regRec.Values["state"], err)
			}
			// A 原子写出归档并裁剪主表。
			if err := txSettings.Save(ctx, repository.SettingsRecord{
				Scope:     receiptScope,
				Values:    map[string]string{"receipt": "committed-from-A"},
				UpdatedAt: now.Add(2 * time.Second),
			}); err != nil {
				return err
			}
			return txSettings.Save(ctx, repository.SettingsRecord{
				Scope:     repository.GuildRegistryScope,
				Values:    map[string]string{"state": "R1-archived-by-A"},
				UpdatedAt: now.Add(2 * time.Second),
			})
		})
	}()

	if bErr := <-bDone; !errors.Is(bErr, context.Canceled) {
		t.Fatalf("expected B canceled rollback, got %v", bErr)
	}
	if aErr := <-aDone; aErr != nil {
		t.Fatalf("A WithinGuildArchive failed: %v", aErr)
	}

	gotArchive, foundArchive, err := baseSettings.Load(ctx, receiptScope)
	if err != nil || !foundArchive || gotArchive.Values["receipt"] != "committed-from-A" {
		t.Fatalf("archive lost or corrupted after mid-commit rollback barrier: found=%v got=%+v err=%v", foundArchive, gotArchive, err)
	}
	gotReg, foundReg, err := baseSettings.Load(ctx, repository.GuildRegistryScope)
	if err != nil || !foundReg || gotReg.Values["state"] != "R1-archived-by-A" {
		t.Fatalf("main registry mismatch after archive commit: found=%v got=%+v err=%v", foundReg, gotReg, err)
	}
}
