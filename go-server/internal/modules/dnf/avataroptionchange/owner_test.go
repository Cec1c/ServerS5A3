package avataroptionchange

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 模块层回归：事务边界、失败分类与"能力值原样落库、服务端不生成"。

type stubCatalog map[uint32]ItemDefinition

func (c stubCatalog) ResolveItem(itemID uint32) (ItemDefinition, error) {
	definition, ok := c[itemID]
	if !ok {
		return ItemDefinition{}, errors.New("item not found")
	}
	return definition, nil
}

func optionChangeTestRepositories(t *testing.T, slots map[string]dnfrepo.ItemStack) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: slots}).Slots,
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func optionChangeTestCatalog() stubCatalog {
	return stubCatalog{
		900: {Kind: KindStackable, AvatarGrades: []int64{3}},
		901: {Kind: KindStackable},
		910: {Kind: KindEquipment, Grade: 3, EquipmentType: "[coat avatar]", IsAvatar: true},
		911: {Kind: KindEquipment, Grade: 2, EquipmentType: "[hair avatar]", IsAvatar: true},
		912: {Kind: KindEquipment, Grade: 3, EquipmentType: "[weapon]"},
	}
}

func optionChangeTestSlots() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 900, Count: 2},
		"1:5": {ItemID: 910, Count: 1, Extra: map[string]string{AbilityNoExtraKey: "0"}},
	}
}

func TestOwnerCommitsSourceAndAbilityInsideOneTransaction(t *testing.T) {
	repositories := optionChangeTestRepositories(t, optionChangeTestSlots())
	owner, err := NewOwner(repositories, optionChangeTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	projected := 0
	result, err := owner.ChangeAvatarOption(context.Background(), Command{
		AccountID:    "account-1",
		CharacterID:  "19",
		SourceSlot:   9,
		SourceItemID: 900,
		TargetSlot:   5,
		TargetItemID: 910,
		AbilityNo:    47,
		ProjectAvatarRow: func(stack *dnfrepo.ItemStack) {
			projected++
			if got := stack.Extra[AbilityNoExtraKey]; got != "47" {
				t.Fatalf("projector ran before the ability was written: %q", got)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if projected != 1 {
		t.Fatalf("projector calls = %d want 1", projected)
	}
	if result.SourceRemaining != 1 || result.SourceRemoved ||
		result.TargetAbilityNo != 47 || result.CharacterID != "19" {
		t.Fatalf("result = %+v", result)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if got := inventory.Slots["0:9"].Count; got != 1 {
		t.Fatalf("persisted source count = %d want 1", got)
	}
	if got := inventory.Slots["1:5"].Extra[AbilityNoExtraKey]; got != "47" {
		t.Fatalf("persisted ability = %q want 47", got)
	}
}

func TestOwnerRejectsWithoutTouchingInventory(t *testing.T) {
	cases := []struct {
		name    string
		command Command
		want    error
	}{
		{
			name:    "缺投影器 = ProjectorRequired（事务不允许半成品写入）",
			command: Command{AccountID: "account-1", CharacterID: "19", SourceSlot: 9, SourceItemID: 900, TargetSlot: 5, TargetItemID: 910},
			want:    ErrProjectorRequired,
		},
		{
			name:    "缺账号 = AccountRequired",
			command: Command{CharacterID: "19", SourceSlot: 9, SourceItemID: 900, TargetSlot: 5, TargetItemID: 910, ProjectAvatarRow: func(*dnfrepo.ItemStack) {}},
			want:    ErrAccountRequired,
		},
		{
			name:    "缺角色 = CharacterRequired",
			command: Command{AccountID: "account-1", SourceSlot: 9, SourceItemID: 900, TargetSlot: 5, TargetItemID: 910, ProjectAvatarRow: func(*dnfrepo.ItemStack) {}},
			want:    ErrCharacterRequired,
		},
		{
			name: "账号不匹配 = AccountMismatch",
			command: Command{
				AccountID: "account-2", CharacterID: "19",
				SourceSlot: 9, SourceItemID: 900, TargetSlot: 5, TargetItemID: 910,
				ProjectAvatarRow: func(*dnfrepo.ItemStack) {},
			},
			want: ErrAccountMismatch,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repositories := optionChangeTestRepositories(t, optionChangeTestSlots())
			owner, err := NewOwner(repositories, optionChangeTestCatalog())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ChangeAvatarOption(context.Background(), test.command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v want %v", err, test.want)
			}
			inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			if got := inventory.Slots["0:9"].Count; got != 2 {
				t.Fatalf("rejected transaction consumed the box: %+v", got)
			}
			if got := inventory.Slots["1:5"].Extra[AbilityNoExtraKey]; got != "0" {
				t.Fatalf("rejected transaction changed the avatar: %q", got)
			}
		})
	}
}

// TestOwnerGradeMatrixIsEqualityOnly 钉住厂商口径：判据是"箱子 AvatarGrade 列表里
// 存在与装扮 [grade] 相等的条目"，而不是"只认稀有"。
func TestOwnerGradeMatrixIsEqualityOnly(t *testing.T) {
	cases := []struct {
		name      string
		boxGrades []int64
		avatar    int64
		want      bool
	}{
		{name: "稀有箱(3) × 稀有装扮(3)", boxGrades: []int64{3}, avatar: 3, want: true},
		{name: "稀有箱(3) × 高级装扮(2)", boxGrades: []int64{3}, avatar: 2, want: false},
		{name: "高级箱(2) × 高级装扮(2)", boxGrades: []int64{2}, avatar: 2, want: true},
		{name: "多行箱子取任意相等行", boxGrades: []int64{2, 3}, avatar: 3, want: true},
		{name: "空列表一律拒绝", boxGrades: nil, avatar: 3, want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := avatarGradeAccepted(test.boxGrades, test.avatar); got != test.want {
				t.Fatalf("avatarGradeAccepted(%v, %d) = %t want %t", test.boxGrades, test.avatar, got, test.want)
			}
		})
	}
}
