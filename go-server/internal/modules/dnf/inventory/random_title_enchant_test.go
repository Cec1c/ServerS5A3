package inventory

// 2017 劳动节随机称号附魔（宝珠 `[stackable type] [auto enchant]` + `[random title enchant]`）
// 的领域层回归。
//
// 解析侧在 dnfbridge（resolveCurrentRandomTitleEnchantMetadata / random_title_enchant_pvf.go），
// 本文件不重复解析：把 EnchantBeadResolution 的随机称号字段直接喂给真实 owner，钉三件事——
//   - 落库：RandomTitleEnchantConfigID/RowID 同时进 Extra 双键与 raw +0x61..+0x65，
//     并把普通卡片字节（+0x0E..+0x12 与 value_a/enchant_card_id/byte_12/enchant_upgrade_count）清零；
//   - 拒绝：半对、混怪物卡、非称号部位、空或错 whitelist、不支持的坐标一律不扣不改；
//   - 守恒与互斥：成功只扣 1 颗，锁定/过期源或目标一颗不扣；之后走普通卡片附魔时
//     random 双键与 raw 随机段必须被清掉。
//
// 夹具沿用 owner_enchant_test.go 的 saveTestInventory/slotKey/newEnchantTestCommand/
// staticEnchantResolver 形状，不新增产品行为镜像。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	randomTitleEnchantCharacterID string = "77"
	randomTitleEnchantBeadSlot    int16  = 12
	randomTitleEnchantTargetSlot  int16  = 30
	randomTitleEnchantBeadItemID  int64  = 8801
	randomTitleEnchantTitleItemID int64  = 700
	randomTitleEnchantPetItemID   int64  = 400990168
	randomTitleEnchantPetSlot     int16  = 24

	randomTitleEnchantConfigID uint16 = 1234
	randomTitleEnchantRowID    uint16 = 5678

	randomTitleEnchantConfigExtra = "random_title_enchant_config_id"
	randomTitleEnchantRowExtra    = "random_title_enchant_row_id"
)

// randomTitleEnchantFixtureOptions 描述一次夹具的起始状态；零值 targetCount 按 1 处理。
type randomTitleEnchantFixtureOptions struct {
	beadCount     int64
	targetCount   int64
	targetCardID  int64
	targetConfig  uint16
	targetRowID   uint16
	beadLocked    bool
	beadExpired   bool
	targetLocked  bool
	targetExpired bool
}

func randomTitleEnchantResolution() alignedcmd.EnchantBeadResolution {
	return alignedcmd.EnchantBeadResolution{
		RandomTitleEnchantConfigID: randomTitleEnchantConfigID,
		RandomTitleEnchantRowID:    randomTitleEnchantRowID,
		TargetWhitelist:            []int64{randomTitleEnchantTitleItemID},
		AllowedEquipmentTypes:      []string{"[title name]"},
		TargetEquipmentType:        "[title name]",
		TargetKind:                 "equipment",
	}
}

// titleCardEnchantResolver 是"普通称号卡片"形状：有怪物卡 ID、没有随机称号字段。
func titleCardEnchantResolver(cardItemID int64) alignedcmd.EnchantBeadResolver {
	return staticEnchantResolver(alignedcmd.EnchantBeadResolution{
		CardItemID:            cardItemID,
		AllowedEquipmentTypes: []string{"[title name]"},
		TargetEquipmentType:   "[title name]",
		TargetKind:            "equipment",
	})
}

func randomTitleEnchantRawIDs(stack dnfrepo.ItemStack) (uint16, uint16) {
	return binary.LittleEndian.Uint16(stack.RawEntry[0x61:0x63]),
		binary.LittleEndian.Uint16(stack.RawEntry[0x63:0x65])
}

func saveRandomTitleEnchantFixture(t *testing.T, ctx context.Context, repos dnfrepo.Group, opts randomTitleEnchantFixtureOptions) {
	t.Helper()
	beadExtra := map[string]string{"item_kind": "stackable"}
	if opts.beadLocked {
		beadExtra["equipment_lock_state"] = "1"
	}
	bead := dnfrepo.ItemStack{
		ItemID:   randomTitleEnchantBeadItemID,
		Count:    opts.beadCount,
		RawEntry: make([]byte, currentItemListEntrySize),
		Extra:    beadExtra,
	}
	if opts.beadExpired {
		bead.ExpireAt = time.Now().Add(-time.Minute)
	}

	targetCount := opts.targetCount
	if targetCount == 0 {
		targetCount = 1
	}
	targetRaw := make([]byte, currentItemListEntrySize)
	targetExtra := map[string]string{"item_kind": "equipment", "equipment_type": "[title name]"}
	if opts.targetCardID > 0 {
		value := uint32(opts.targetCardID)
		targetRaw[0x0E] = byte(value)
		targetRaw[0x0F] = byte(value >> 8)
		targetRaw[0x10] = byte(value >> 16)
		targetRaw[0x11] = byte(value >> 24)
		targetRaw[0x12] = 1
		targetExtra["value_a"] = strconv.FormatInt(opts.targetCardID, 10)
		targetExtra["enchant_card_id"] = strconv.FormatInt(opts.targetCardID, 10)
		targetExtra["byte_12"] = "1"
		targetExtra["enchant_upgrade_count"] = "1"
	}
	if opts.targetConfig != 0 {
		binary.LittleEndian.PutUint16(targetRaw[0x61:0x63], opts.targetConfig)
		targetExtra[randomTitleEnchantConfigExtra] = strconv.FormatUint(uint64(opts.targetConfig), 10)
	}
	if opts.targetRowID != 0 {
		binary.LittleEndian.PutUint16(targetRaw[0x63:0x65], opts.targetRowID)
		targetExtra[randomTitleEnchantRowExtra] = strconv.FormatUint(uint64(opts.targetRowID), 10)
	}
	if opts.targetLocked {
		targetExtra["equipment_lock_state"] = "1"
	}
	target := dnfrepo.ItemStack{
		ItemID:   randomTitleEnchantTitleItemID,
		Count:    targetCount,
		RawEntry: targetRaw,
		Extra:    targetExtra,
	}
	if opts.targetExpired {
		target.ExpireAt = time.Now().Add(-time.Minute)
	}

	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: randomTitleEnchantCharacterID,
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, randomTitleEnchantBeadSlot):   bead,
			slotKey(listTypeMain, randomTitleEnchantTargetSlot): target,
		},
	})
}

// saveRandomTitleEnchantPetLaneFixture 是宠物方向的最小夹具（宝珠在 0:12，宠物本体在
// list 7 槽 24，并写入同坐标 PetEntry），形状与 savePetEnchantFixture 一致。
func saveRandomTitleEnchantPetLaneFixture(t *testing.T, ctx context.Context, repos dnfrepo.Group, beadCount int64) {
	t.Helper()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: randomTitleEnchantCharacterID,
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, randomTitleEnchantBeadSlot): {
				ItemID:   randomTitleEnchantBeadItemID,
				Count:    beadCount,
				RawEntry: make([]byte, currentItemListEntrySize),
				Extra:    map[string]string{"item_kind": "stackable"},
			},
			slotKey(listTypePet, randomTitleEnchantPetSlot): {
				ItemID:   randomTitleEnchantPetItemID,
				Count:    1,
				RawEntry: make([]byte, currentItemListEntrySize),
				Extra: map[string]string{
					"creature_serial_or_handle": "37",
					"creature_key":              "37",
					"item_kind":                 "equipment",
					"equipment_type":            "[creature]",
				},
			},
		},
	})
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: randomTitleEnchantCharacterID,
		Entries: map[string]dnfrepo.PetEntry{
			"37": {
				PetKey:          "37",
				CreatureKey:     37,
				ItemID:          randomTitleEnchantPetItemID,
				SourceListType:  listTypePet,
				SourceSlotIndex: randomTitleEnchantPetSlot,
				Level:           1,
				Satiety:         100,
				RawEntry:        make([]byte, currentItemListEntrySize),
			},
		},
	}); err != nil {
		t.Fatalf("Save pet error = %v", err)
	}
}

// TestRandomTitleEnchantPersistsConfigRowAndClearsCard 钉成功路径的落库形状：config/row
// 双字段同时进 Extra 与 raw +0x61..+0x65，原有普通卡片字节清零，且只扣一颗宝珠。
func TestRandomTitleEnchantPersistsConfigRowAndClearsCard(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveRandomTitleEnchantFixture(t, ctx, repos, randomTitleEnchantFixtureOptions{beadCount: 2, targetCardID: 9001})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Enchant(ctx, newEnchantTestCommand(randomTitleEnchantBeadSlot, randomTitleEnchantTargetSlot), staticEnchantResolver(randomTitleEnchantResolution()))
	if err != nil {
		t.Fatalf("Enchant error = %v", err)
	}
	if !result.Success || !result.Changed {
		t.Fatalf("result = %+v, want success", result)
	}
	if result.CardItemID != 0 || result.EnchantUpgradeCount != 0 {
		t.Fatalf("random title enchant must not report a monster card: %+v", result)
	}
	if result.BeadRemainingCount != 1 {
		t.Fatalf("bead remaining = %d, want 1 (exactly one bead is consumed)", result.BeadRemainingCount)
	}

	loaded := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
	target := loaded.Slots[slotKey(listTypeMain, randomTitleEnchantTargetSlot)]
	if got := target.Extra[randomTitleEnchantConfigExtra]; got != "1234" {
		t.Fatalf("target config extra = %q, want 1234", got)
	}
	if got := target.Extra[randomTitleEnchantRowExtra]; got != "5678" {
		t.Fatalf("target row extra = %q, want 5678", got)
	}
	config, row := randomTitleEnchantRawIDs(target)
	if config != randomTitleEnchantConfigID || row != randomTitleEnchantRowID {
		t.Fatalf("raw +0x61..+0x65 = %d/%d, want %d/%d", config, row, randomTitleEnchantConfigID, randomTitleEnchantRowID)
	}
	if !bytes.Equal(target.RawEntry[0x0E:0x13], make([]byte, 5)) {
		t.Fatalf("ordinary card bytes +0x0E..+0x12 = %x, want zero", target.RawEntry[0x0E:0x13])
	}
	for _, key := range []string{"value_a", "enchant_card_id", "byte_12", "enchant_upgrade_count"} {
		if got := target.Extra[key]; got != "0" {
			t.Fatalf("target extra %s = %q, want 0", key, got)
		}
	}
	if got := target.Extra["raw_entry_hex"]; got != hex.EncodeToString(target.RawEntry) {
		t.Fatalf("raw_entry_hex cache = %q, want %q", got, hex.EncodeToString(target.RawEntry))
	}

	bead := loaded.Slots[slotKey(listTypeMain, randomTitleEnchantBeadSlot)]
	if bead.Count != 1 {
		t.Fatalf("bead count = %d, want 1", bead.Count)
	}
	if got := binary.LittleEndian.Uint32(bead.RawEntry[0x06:0x0A]); got != 1 {
		t.Fatalf("bead raw amount = %d, want 1", got)
	}
}

// TestRandomTitleEnchantRejectionsDoNotConsumeOrWrite 钉解析器给出不完整或互相矛盾的随机
// 称号结果、或坐标不在这条支路支持范围内时的拒绝：状态一字节不改，宝珠一颗不扣。
func TestRandomTitleEnchantRejectionsDoNotConsumeOrWrite(t *testing.T) {
	ctx := context.Background()
	mutate := func(apply func(*alignedcmd.EnchantBeadResolution)) alignedcmd.EnchantBeadResolution {
		resolution := randomTitleEnchantResolution()
		apply(&resolution)
		return resolution
	}
	tests := []struct {
		name          string
		resolution    alignedcmd.EnchantBeadResolution
		mutateCmd     func(*Command)
		wantErrorCode byte
	}{
		{
			name:          "config without row",
			resolution:    mutate(func(r *alignedcmd.EnchantBeadResolution) { r.RandomTitleEnchantRowID = 0 }),
			wantErrorCode: enchantErrorInvalidBead,
		},
		{
			name:          "row without config",
			resolution:    mutate(func(r *alignedcmd.EnchantBeadResolution) { r.RandomTitleEnchantConfigID = 0 }),
			wantErrorCode: enchantErrorInvalidBead,
		},
		{
			name:          "random ids mixed with a monster card id",
			resolution:    mutate(func(r *alignedcmd.EnchantBeadResolution) { r.CardItemID = 9001 }),
			wantErrorCode: enchantErrorInvalidBead,
		},
		{
			name: "target is not a title part",
			resolution: mutate(func(r *alignedcmd.EnchantBeadResolution) {
				r.TargetEquipmentType = "[coat]"
				r.AllowedEquipmentTypes = []string{"[coat]"}
			}),
			wantErrorCode: enchantErrorInvalidBead,
		},
		{
			name:          "empty target whitelist",
			resolution:    mutate(func(r *alignedcmd.EnchantBeadResolution) { r.TargetWhitelist = nil }),
			wantErrorCode: enchantErrorInvalidBead,
		},
		{
			name:          "target outside the random whitelist",
			resolution:    mutate(func(r *alignedcmd.EnchantBeadResolution) { r.TargetWhitelist = []int64{111, 222} }),
			wantErrorCode: enchantErrorInvalidTarget,
		},
		{
			name:          "bead is not in the main inventory",
			resolution:    randomTitleEnchantResolution(),
			mutateCmd:     func(cmd *Command) { cmd.BeadListType = listTypePet },
			wantErrorCode: enchantErrorUnsupported,
		},
		{
			name:          "target is neither the main inventory nor the pet lane",
			resolution:    randomTitleEnchantResolution(),
			mutateCmd:     func(cmd *Command) { cmd.TargetListType = listTypeEquipment },
			wantErrorCode: enchantErrorUnsupported,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			saveRandomTitleEnchantFixture(t, ctx, repos, randomTitleEnchantFixtureOptions{beadCount: 2})
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			before := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
			cmd := newEnchantTestCommand(randomTitleEnchantBeadSlot, randomTitleEnchantTargetSlot)
			if tc.mutateCmd != nil {
				tc.mutateCmd(&cmd)
			}
			result, err := owner.Enchant(ctx, cmd, staticEnchantResolver(tc.resolution))
			if err != nil {
				t.Fatalf("Enchant error = %v", err)
			}
			if result.Success || result.Changed || result.ErrorCode != tc.wantErrorCode {
				t.Fatalf("result = %+v, want a refusal with code 0x%02X", result, tc.wantErrorCode)
			}
			after := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
			if bead := after.Slots[slotKey(listTypeMain, randomTitleEnchantBeadSlot)]; bead.Count != 2 {
				t.Fatalf("bead count = %d, want 2 (a refusal must not consume)", bead.Count)
			}
			if !reflect.DeepEqual(before.Slots, after.Slots) {
				t.Fatalf("refused random title enchant mutated the inventory | before=%+v | after=%+v", before.Slots, after.Slots)
			}
		})
	}
}

// TestRandomTitleEnchantRejectsLockedOrExpiredSourceWithoutConsuming 钉随机称号支路自己的
// 锁/过期门：源宝珠或目标称号任一被锁或已过期，都拒绝且一颗不扣、一字节不写。
func TestRandomTitleEnchantRejectsLockedOrExpiredSourceWithoutConsuming(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		fixture randomTitleEnchantFixtureOptions
	}{
		{name: "locked bead", fixture: randomTitleEnchantFixtureOptions{beadCount: 2, beadLocked: true}},
		{name: "expired bead", fixture: randomTitleEnchantFixtureOptions{beadCount: 2, beadExpired: true}},
		{name: "locked title", fixture: randomTitleEnchantFixtureOptions{beadCount: 2, targetLocked: true}},
		{name: "expired title", fixture: randomTitleEnchantFixtureOptions{beadCount: 2, targetExpired: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			saveRandomTitleEnchantFixture(t, ctx, repos, tc.fixture)
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			before := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
			result, err := owner.Enchant(ctx, newEnchantTestCommand(randomTitleEnchantBeadSlot, randomTitleEnchantTargetSlot), staticEnchantResolver(randomTitleEnchantResolution()))
			if err != nil {
				t.Fatalf("Enchant error = %v", err)
			}
			if result.Success || result.Changed || result.ErrorCode != enchantErrorInvalidTarget {
				t.Fatalf("result = %+v, want a refusal with code 0x%02X", result, enchantErrorInvalidTarget)
			}
			after := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
			if bead := after.Slots[slotKey(listTypeMain, randomTitleEnchantBeadSlot)]; bead.Count != 2 {
				t.Fatalf("bead count = %d, want 2 (a refusal must not consume)", bead.Count)
			}
			if !reflect.DeepEqual(before.Slots, after.Slots) {
				t.Fatalf("refused random title enchant mutated the inventory | before=%+v | after=%+v", before.Slots, after.Slots)
			}
		})
	}
}

// TestRandomTitleEnchantRejectsPetLaneTargetWithoutMutation 钉随机称号只走主背包坐标：
// 即使宠物方向坐标给全，也必须在解析器结果进门前拒绝，宝珠与宠物记录都不动。
func TestRandomTitleEnchantRejectsPetLaneTargetWithoutMutation(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveRandomTitleEnchantPetLaneFixture(t, ctx, repos, 2)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	before := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
	beforePet, found, err := repos.Pet.Load(ctx, randomTitleEnchantCharacterID)
	if err != nil || !found {
		t.Fatalf("Load pet found=%t err=%v", found, err)
	}
	cmd := newEnchantTestCommand(randomTitleEnchantBeadSlot, randomTitleEnchantPetSlot)
	cmd.TargetListType = listTypePet
	result, err := owner.Enchant(ctx, cmd, staticEnchantResolver(randomTitleEnchantResolution()))
	if err != nil {
		t.Fatalf("Enchant error = %v", err)
	}
	if result.Success || result.Changed {
		t.Fatalf("result = %+v, want the random title lane to refuse a pet lane target", result)
	}
	after := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
	if bead := after.Slots[slotKey(listTypeMain, randomTitleEnchantBeadSlot)]; bead.Count != 2 {
		t.Fatalf("bead count = %d, want 2 (a refusal must not consume)", bead.Count)
	}
	if !reflect.DeepEqual(before.Slots, after.Slots) {
		t.Fatalf("refused random title enchant mutated the inventory | before=%+v | after=%+v", before.Slots, after.Slots)
	}
	afterPet, found, err := repos.Pet.Load(ctx, randomTitleEnchantCharacterID)
	if err != nil || !found {
		t.Fatalf("Load pet found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(beforePet.Entries, afterPet.Entries) {
		t.Fatalf("refused random title enchant mutated the pet record | before=%+v | after=%+v", beforePet.Entries, afterPet.Entries)
	}
}

// TestRandomTitleEnchantOrdinaryCardClearsRandomConfig 钉两个写面互斥：随机称号附魔之后
// 再打普通称号卡片，random 双键必须从 Extra 删除、raw +0x61..+0x65 必须清零，且缓存
// raw_entry_hex 与 RawEntry 一致，宝珠同样只扣 1。
func TestRandomTitleEnchantOrdinaryCardClearsRandomConfig(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveRandomTitleEnchantFixture(t, ctx, repos, randomTitleEnchantFixtureOptions{
		beadCount:    2,
		targetConfig: randomTitleEnchantConfigID,
		targetRowID:  randomTitleEnchantRowID,
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	before := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
	beforeTarget := before.Slots[slotKey(listTypeMain, randomTitleEnchantTargetSlot)]
	if config, row := randomTitleEnchantRawIDs(beforeTarget); config != randomTitleEnchantConfigID || row != randomTitleEnchantRowID {
		t.Fatalf("fixture raw random ids = %d/%d, want %d/%d", config, row, randomTitleEnchantConfigID, randomTitleEnchantRowID)
	}

	result, err := owner.Enchant(ctx, newEnchantTestCommand(randomTitleEnchantBeadSlot, randomTitleEnchantTargetSlot), titleCardEnchantResolver(9001))
	if err != nil {
		t.Fatalf("Enchant error = %v", err)
	}
	if !result.Success || !result.Changed || result.CardItemID != 9001 {
		t.Fatalf("result = %+v, want the ordinary title card to be applied", result)
	}

	loaded := loadTestInventory(t, ctx, repos, randomTitleEnchantCharacterID)
	target := loaded.Slots[slotKey(listTypeMain, randomTitleEnchantTargetSlot)]
	if got := target.Extra["value_a"]; got != "9001" {
		t.Fatalf("target value_a = %q, want 9001", got)
	}
	if got := target.Extra["enchant_card_id"]; got != "9001" {
		t.Fatalf("target enchant_card_id = %q, want 9001", got)
	}
	if _, ok := target.Extra[randomTitleEnchantConfigExtra]; ok {
		t.Fatalf("random title config extra survived an ordinary enchant: %+v", target.Extra)
	}
	if _, ok := target.Extra[randomTitleEnchantRowExtra]; ok {
		t.Fatalf("random title row extra survived an ordinary enchant: %+v", target.Extra)
	}
	if config, row := randomTitleEnchantRawIDs(target); config != 0 || row != 0 {
		t.Fatalf("raw random ids survived an ordinary enchant: %d/%d", config, row)
	}
	if got := target.Extra["raw_entry_hex"]; got != hex.EncodeToString(target.RawEntry) {
		t.Fatalf("raw_entry_hex cache = %q, want %q", got, hex.EncodeToString(target.RawEntry))
	}
	if bead := loaded.Slots[slotKey(listTypeMain, randomTitleEnchantBeadSlot)]; bead.Count != 1 {
		t.Fatalf("bead count = %d, want 1", bead.Count)
	}
}
