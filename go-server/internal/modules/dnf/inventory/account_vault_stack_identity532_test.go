// P61/P68 账号金库堆叠（独立候选 532）判据与端到端回归。
//
// 范围：internal/modules/dnf/inventory 的账号金库 list12 移动路径。
//   - 同种道具（同 item/bind/实例到期/PVF 契约/身份键）正常并成一行；
//   - 声明身份键 stackable_type / expire_time / expire_unix 两边都写且不同时不并；
//   - 合法容量边界（[stack limit] 精确可容 / 超出拒绝）与无上限档保持不变；
//   - 只差发放记账键（source / last_grant_source）仍可合并。
//
// 真实 PVF 合同（固定 Script.pvf 读回 417）：[waste]，StackLimit 1000，
// 主背包 65..120；本文件用同一形状的 Extra，不伪造 417 的材料页身份。
// 本文件不覆盖 SQL 并发、真实客户端入库时序与整理合并，那些边界见交接报告。
package inventory

import (
	"context"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	accountVault532AccountID   = "synthetic-account-vault-532"
	accountVault532CharacterID = 77
	accountVault532ItemID      = 417
	accountVault532PVFPath     = "stackable/event/chn_event_tau_2009/chn_tau_vita.stk"
	accountVault532Stackable   = "[waste]"
)

func accountVault532Stack(t *testing.T, count int64, limit int64) dnfrepo.ItemStack {
	t.Helper()
	return dnfrepo.ItemStack{
		ItemID: accountVault532ItemID,
		Count:  count,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       accountVault532PVFPath,
			"stackable_type": accountVault532Stackable,
			"stack_limit":    strconv.FormatInt(limit, 10),
		},
	}
}

func accountVault532Group(t *testing.T) (dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountVault532AccountID,
		Metadata: map[string]string{
			"account_cargo_created": "true",
			"account_cargo_level":   "8",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Character + Inventory records are seeded by the caller; only the account
	// cargo owner needs the account row to exist up front.
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner
}

func accountVault532Cargo(t *testing.T, repos dnfrepo.Group) dnfrepo.AccountInventoryRecord {
	t.Helper()
	cargo, found, err := repos.AccountInventory.Load(context.Background(), accountVault532AccountID)
	if err != nil || !found {
		t.Fatalf("account cargo missing: found=%t err=%v", found, err)
	}
	return cargo
}

func TestAccountVaultCanStack532CapacityBoundaryAndIdentityKeys(t *testing.T) {
	base := func(t *testing.T) (dnfrepo.ItemStack, dnfrepo.ItemStack) {
		return accountVault532Stack(t, 3, 1000), accountVault532Stack(t, 993, 1000)
	}
	tests := []struct {
		name        string
		mutate      func(*dnfrepo.ItemStack, *dnfrepo.ItemStack)
		moveCount   int64
		wantAllowed bool
	}{
		{name: "same_kind_and_metadata_stacks", moveCount: 3, wantAllowed: true},
		{name: "exactly_fills_recorded_limit", moveCount: 7, wantAllowed: true,
			mutate: func(source, _ *dnfrepo.ItemStack) { source.Count = 7 }},
		{name: "exceeds_recorded_limit", moveCount: 8, wantAllowed: false,
			mutate: func(source, _ *dnfrepo.ItemStack) { source.Count = 8 }},
		{name: "explicit_different_stack_limit_refuses", moveCount: 3, wantAllowed: false,
			mutate: func(_, destination *dnfrepo.ItemStack) { destination.Extra["stack_limit"] = "999" }},
		{name: "omitted_stack_limit_is_uncapped", moveCount: 3, wantAllowed: true,
			mutate: func(source, destination *dnfrepo.ItemStack) {
				delete(source.Extra, "stack_limit")
				delete(destination.Extra, "stack_limit")
			}},
		{name: "different_stackable_type_refuses", moveCount: 3, wantAllowed: false,
			mutate: func(source, destination *dnfrepo.ItemStack) {
				source.Extra["stackable_type"] = "[material]"
				destination.Extra["stackable_type"] = "[waste]"
			}},
		{name: "different_expire_time_refuses", moveCount: 3, wantAllowed: false,
			mutate: func(source, destination *dnfrepo.ItemStack) {
				source.Extra["expire_time"] = "1900000001"
				destination.Extra["expire_time"] = "1900000002"
			}},
		{name: "different_expire_unix_refuses", moveCount: 3, wantAllowed: false,
			mutate: func(source, destination *dnfrepo.ItemStack) {
				source.Extra["expire_unix"] = "1900000001"
				destination.Extra["expire_unix"] = "1900000002"
			}},
		{name: "one_sided_identity_key_is_compatible", moveCount: 3, wantAllowed: true,
			mutate: func(_, destination *dnfrepo.ItemStack) { delete(destination.Extra, "stackable_type") }},
		{name: "attribution_keys_do_not_block", moveCount: 3, wantAllowed: true,
			mutate: func(source, destination *dnfrepo.ItemStack) {
				source.Extra["source"] = "mail-normal"
				destination.Extra["last_grant_source"] = "public-test-attribution"
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, destination := base(t)
			if test.mutate != nil {
				test.mutate(&source, &destination)
			}
			if got := canStack(source, destination, test.moveCount); got != test.wantAllowed {
				t.Fatalf("canStack = %t, want %t; source=%+v destination=%+v", got, test.wantAllowed, source, destination)
			}
		})
	}
}

func TestAccountVault532SameKindDepositStacksIntoOneRow(t *testing.T) {
	ctx := context.Background()
	repos, owner := accountVault532Group(t)
	source := accountVault532Stack(t, 7, 1000)
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{slotKey(listTypeMain, 65): source},
	})
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: accountVault532AccountID,
		Slots:     map[string]dnfrepo.ItemStack{slotKey(listTypeAccountCargo, 0): accountVault532Stack(t, 5, 1000)},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := owner.Move(ctx, Command{
		AccountID:            accountVault532AccountID,
		SelectedCharacterID:  accountVault532CharacterID,
		SourceListType:       listTypeMain,
		SourceSlotIndex:      65,
		MoveCount:            7,
		DestinationListType:  listTypeAccountCargo,
		DestinationSlotIndex: 0,
	})
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "stack" || result.MoveCount != 7 || !result.Changed {
		t.Fatalf("result = %+v", result)
	}
	character := loadTestInventory(t, ctx, repos, "77")
	if _, exists := character.Slots[slotKey(listTypeMain, 65)]; exists {
		t.Fatalf("source row survived: %+v", character.Slots)
	}
	cargo := accountVault532Cargo(t, repos)
	if len(cargo.Slots) != 1 {
		t.Fatalf("account vault rows = %d, want 1: %+v", len(cargo.Slots), cargo.Slots)
	}
	merged := cargo.Slots[slotKey(listTypeAccountCargo, 0)]
	if merged.ItemID != accountVault532ItemID || merged.Count != 12 {
		t.Fatalf("merged row = %+v, want item=%d count=12", merged, accountVault532ItemID)
	}
	if merged.Extra["stack_limit"] != "1000" || merged.Extra["stackable_type"] != accountVault532Stackable {
		t.Fatalf("merged identity/cap lost: %+v", merged.Extra)
	}
}

func TestAccountVault532DifferentMetadataDoesNotMerge(t *testing.T) {
	cases := []struct{ key, existingValue, incomingValue string }{
		{key: "stackable_type", existingValue: "[waste]", incomingValue: "[material]"},
		{key: "expire_time", existingValue: "1900000001", incomingValue: "1900000002"},
		{key: "expire_unix", existingValue: "1900000001", incomingValue: "1900000002"},
	}
	for _, testCase := range cases {
		t.Run(testCase.key, func(t *testing.T) {
			ctx := context.Background()
			repos, owner := accountVault532Group(t)
			source := accountVault532Stack(t, 5, 1000)
			source.Extra[testCase.key] = testCase.incomingValue
			existing := accountVault532Stack(t, 7, 1000)
			existing.Extra[testCase.key] = testCase.existingValue
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots:       map[string]dnfrepo.ItemStack{slotKey(listTypeMain, 65): source},
			})
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
				AccountID: accountVault532AccountID,
				Slots:     map[string]dnfrepo.ItemStack{slotKey(listTypeAccountCargo, 0): existing},
			}); err != nil {
				t.Fatal(err)
			}

			result, err := owner.Move(ctx, Command{
				AccountID:            accountVault532AccountID,
				SelectedCharacterID:  accountVault532CharacterID,
				SourceListType:       listTypeMain,
				SourceSlotIndex:      65,
				MoveCount:            5,
				DestinationListType:  listTypeAccountCargo,
				DestinationSlotIndex: 1,
			})
			if err != nil {
				t.Fatalf("Move error = %v", err)
			}
			if result.Mode != "move" {
				t.Fatalf("identity conflict merged: result = %+v", result)
			}
			cargo := accountVault532Cargo(t, repos)
			if len(cargo.Slots) != 2 {
				t.Fatalf("account vault rows = %d, want 2: %+v", len(cargo.Slots), cargo.Slots)
			}
			if got := cargo.Slots[slotKey(listTypeAccountCargo, 0)]; got.Count != 7 || got.Extra[testCase.key] != testCase.existingValue {
				t.Fatalf("existing row mutated: %+v", got)
			}
			if got := cargo.Slots[slotKey(listTypeAccountCargo, 1)]; got.Count != 5 || got.Extra[testCase.key] != testCase.incomingValue {
				t.Fatalf("incoming row lost its identity: %+v", got)
			}
		})
	}
}
