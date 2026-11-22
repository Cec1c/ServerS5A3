package repository_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestP0GuildArchiveScopeValidation(t *testing.T) {
	validReq := fmt.Sprintf("%064x", 0xabc123)
	if scope := repository.GuildReceiptArchiveScope(validReq); !repository.ValidGuildReceiptArchiveScope(scope) || !repository.ValidGuildArchiveScope(scope) {
		t.Fatalf("expected valid receipt archive scope, got %q", scope)
	}
	for _, bad := range []string{
		"",
		"short",
		strings.ToUpper(validReq),
		strings.Repeat("g", 64),
		validReq + "0",
	} {
		if scope := repository.GuildReceiptArchiveScope(bad); scope != "" {
			t.Fatalf("expected empty scope for invalid requestID %q, got %q", bad, scope)
		}
		if repository.ValidGuildReceiptArchiveScope("guild-archive:v1:receipt:" + bad) {
			t.Fatalf("unexpectedly accepted invalid receipt scope suffix %q", bad)
		}
	}

	for _, id := range []uint32{1, 42, ^uint32(0)} {
		scope := repository.GuildHongbaoArchiveScope(id)
		if !repository.ValidGuildHongbaoArchiveScope(scope) || !repository.ValidGuildArchiveScope(scope) {
			t.Fatalf("expected valid hongbao archive scope for id=%d, got %q", id, scope)
		}
	}
	dayEnd := time.Date(2026, 11, 21, 22, 0, 0, 0, time.UTC)
	dayScope := repository.GuildContractDayArchiveScope(repository.GuildOwner{AccountID: "a1", CharacterID: "1"}, dayEnd)
	if !repository.ValidGuildContractDayArchiveScope(dayScope) || !repository.ValidGuildArchiveScope(dayScope) {
		t.Fatalf("expected valid contract-day archive scope, got %q", dayScope)
	}
	if repository.GuildContractDayArchiveScope(repository.GuildOwner{AccountID: "", CharacterID: "1"}, dayEnd) != "" ||
		repository.GuildContractDayArchiveScope(repository.GuildOwner{AccountID: "a1", CharacterID: "1"}, time.Time{}) != "" {
		t.Fatal("invalid contract-day inputs must return empty scope")
	}
	if repository.ValidGuildContractDayArchiveScope("guild-archive:v1:contract-day:short") {
		t.Fatal("unexpectedly accepted short contract-day archive scope")
	}
	if repository.GuildHongbaoArchiveScope(0) != "" {
		t.Fatal("id=0 must return empty hongbao archive scope")
	}
	for _, bad := range []string{
		"guild-archive:v1:hongbao:",
		"guild-archive:v1:hongbao:0",
		"guild-archive:v1:hongbao:01",
		"guild-archive:v1:hongbao:-1",
		"guild-archive:v1:hongbao:4294967296",
		"guild-archive:v1:hongbao:abc",
		"global:guild_registry:v1",
		"other:scope",
	} {
		if repository.ValidGuildHongbaoArchiveScope(bad) {
			t.Fatalf("unexpectedly accepted invalid hongbao archive scope %q", bad)
		}
	}
}

func TestP0MemoryGuildTransactionsSupportAtomicArchiveAndRollback(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	owner := repository.GuildOwner{AccountID: "a1", CharacterID: "1"}
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	if err := group.Account.Save(ctx, repository.AccountRecord{AccountID: owner.AccountID}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, repository.CharacterRecord{CharacterID: owner.CharacterID, AccountID: owner.AccountID, Name: "c1"}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: owner.CharacterID}); err != nil {
		t.Fatal(err)
	}

	receiptScope := repository.GuildReceiptArchiveScope(fmt.Sprintf("%064x", 7))
	hongbaoScope := repository.GuildHongbaoArchiveScope(9)
	preExistingScope := repository.GuildHongbaoArchiveScope(3)
	preExisting := repository.SettingsRecord{
		Scope:     preExistingScope,
		Values:    map[string]string{"hongbao": "before"},
		UpdatedAt: now,
	}
	if err := group.Settings.Save(ctx, preExisting); err != nil {
		t.Fatal(err)
	}

	// 1. 回滚验证：WithinGuild 中途报错时，新建归档应被移除，既有归档应恢复原值，越权 scope 应被拒绝。
	boom := errors.New("rollback archive")
	err := group.Guild.WithinGuild(ctx, []repository.GuildOwner{owner}, func(tx repository.Group) error {
		if _, _, loadErr := tx.Settings.Load(ctx, "unauthorized:scope"); !errors.Is(loadErr, repository.ErrGuildTransaction) {
			t.Fatalf("expected ErrGuildTransaction for unauthorized scope, got %v", loadErr)
		}
		if err := tx.Settings.Save(ctx, repository.SettingsRecord{Scope: receiptScope, Values: map[string]string{"receipt": "temp"}, UpdatedAt: now}); err != nil {
			return err
		}
		if err := tx.Settings.Save(ctx, repository.SettingsRecord{Scope: preExistingScope, Values: map[string]string{"hongbao": "mutated"}, UpdatedAt: now}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
	if _, found, err := group.Settings.Load(ctx, receiptScope); err != nil || found {
		t.Fatalf("rolled-back receipt archive still exists: found=%v err=%v", found, err)
	}
	if got, found, err := group.Settings.Load(ctx, preExistingScope); err != nil || !found || !reflect.DeepEqual(got.Values, preExisting.Values) {
		t.Fatalf("rolled-back pre-existing archive mismatch: found=%v got=%+v err=%v", found, got, err)
	}

	// 2. 提交验证：WithinGuildInventory 与 WithinGuildSettlement 均可原子写入受控归档 scope。
	invUOW := group.Guild.(repository.GuildInventoryUnitOfWork)
	if err := invUOW.WithinGuildInventory(ctx, owner, func(tx repository.Group) error {
		return tx.Settings.Save(ctx, repository.SettingsRecord{Scope: receiptScope, Values: map[string]string{"receipt": "committed"}, UpdatedAt: now})
	}); err != nil {
		t.Fatalf("WithinGuildInventory archive save failed: %v", err)
	}
	if got, found, err := group.Settings.Load(ctx, receiptScope); err != nil || !found || got.Values["receipt"] != "committed" {
		t.Fatalf("committed receipt archive mismatch: found=%v got=%+v err=%v", found, got, err)
	}

	settleUOW := group.CharacterSettlement.(repository.GuildSettlementUnitOfWork)
	if err := settleUOW.WithinGuildSettlement(ctx, owner, "p0-archive-check", func(tx repository.Group) error {
		if _, _, loadErr := tx.Settings.Load(ctx, "unauthorized:scope"); !errors.Is(loadErr, repository.ErrGuildTransaction) {
			t.Fatalf("expected ErrGuildTransaction for unauthorized scope in settlement, got %v", loadErr)
		}
		return tx.Settings.Save(ctx, repository.SettingsRecord{Scope: hongbaoScope, Values: map[string]string{"hongbao": "archived-hb"}, UpdatedAt: now})
	}); err != nil {
		t.Fatalf("WithinGuildSettlement archive save failed: %v", err)
	}
	if got, found, err := group.Settings.Load(ctx, hongbaoScope); err != nil || !found || got.Values["hongbao"] != "archived-hb" {
		t.Fatalf("committed hongbao archive mismatch: found=%v got=%+v err=%v", found, got, err)
	}

	// 3. WithinGuildArchive 原子提交与回滚验证。
	archiver := group.Guild.(repository.GuildArchiveUnitOfWork)
	dayScope := repository.GuildContractDayArchiveScope(owner, now)
	if err := archiver.WithinGuildArchive(ctx, func(txSettings repository.SettingsRepository) error {
		if _, _, loadErr := txSettings.Load(ctx, "unauthorized:scope"); !errors.Is(loadErr, repository.ErrGuildTransaction) {
			t.Fatalf("expected ErrGuildTransaction for unauthorized scope in WithinGuildArchive, got %v", loadErr)
		}
		if err := txSettings.Save(ctx, repository.SettingsRecord{Scope: dayScope, Values: map[string]string{"contract_day": "temp"}, UpdatedAt: now}); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("expected boom rollback from WithinGuildArchive, got %v", err)
	}
	if _, found, err := group.Settings.Load(ctx, dayScope); err != nil || found {
		t.Fatalf("rolled-back contract-day archive still exists: found=%v err=%v", found, err)
	}
	if err := archiver.WithinGuildArchive(ctx, func(txSettings repository.SettingsRepository) error {
		return txSettings.Save(ctx, repository.SettingsRecord{Scope: dayScope, Values: map[string]string{"contract_day": "committed"}, UpdatedAt: now})
	}); err != nil {
		t.Fatalf("WithinGuildArchive commit failed: %v", err)
	}
	if got, found, err := group.Settings.Load(ctx, dayScope); err != nil || !found || got.Values["contract_day"] != "committed" {
		t.Fatalf("committed contract-day archive mismatch: found=%v got=%+v err=%v", found, got, err)
	}
}
