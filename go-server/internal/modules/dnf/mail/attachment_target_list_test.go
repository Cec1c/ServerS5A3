package mail

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 宠物更名卡（item 25，PVF [stackable type] [creature]）必须领取进宠物背包
// list 7 的宠物消耗品段：主背包没有它的合法槽位，玩家把卡放在 list 0 时
// 客户端既看不到也用不了（rename.go 要求 list 7）。库里的真实行曾经落在
// 0:65 和 0:80，就是这条路径把它放进主背包造成的。
func TestAddAttachmentToInventoryPlacesCreatureStackableInPetList(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}
	attachment := dnfrepo.MailAttachment{ItemID: 25, Count: 5}
	resolution := alignedcmd.MailboxItemResolution{
		Kind:           "stackable",
		PVFPath:        "stackable/cash/creature/rename_card.stk",
		SlotStart:      189,
		SlotEnd:        238,
		StackLimit:     1000,
		TargetListType: 7,
	}
	refreshes, err := addAttachmentToInventory(inventory, attachment, resolution)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	stack, ok := inventory.Slots["7:189"]
	if !ok || stack.ItemID != 25 || stack.Count != 5 {
		t.Fatalf("pet slot=%+v ok=%t", stack, ok)
	}
	if _, occupied := inventory.Slots["0:189"]; occupied {
		t.Fatal("[creature] stackable must not be placed in the main list")
	}
	if len(refreshes) != 1 || refreshes[0].ListType != 7 || refreshes[0].SlotIndex != 189 {
		t.Fatalf("refreshes=%+v", refreshes)
	}
}

// 附件只带 itemID 时，dnfbridge 按运行时 PVF 给出的创建期徽章孔必须落到装扮栏
// 条目的孔位向量上（Extra 的 avatar_socket_data）。当前客户端把装扮栏(list 1)的
// 孔位放在固定 0x77 行之后的可选 blob 里，由 dnfbridge 从 Extra 生成，所以写
// RawEntry 是没用的——这正是"可镶嵌皮肤没有镶嵌孔"的成因。
func TestAddAttachmentToInventoryKeepsAvatarSocketData(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}
	socketData := []byte{0xEF, 0xFF, 0, 0, 0, 0, 0xEF, 0xFF, 0, 0, 0, 0}
	resolution := alignedcmd.MailboxItemResolution{
		Kind:             "equipment",
		SlotStart:        0,
		SlotEnd:          500,
		StackLimit:       1,
		TargetListType:   1,
		AvatarSocketData: socketData,
	}
	refreshes, err := addAttachmentToInventory(inventory, dnfrepo.MailAttachment{ItemID: 415580009, Count: 1}, resolution)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	stack, ok := inventory.Slots["1:0"]
	if !ok {
		t.Fatalf("avatar attachment must land in the avatar list, slots=%v", inventory.Slots)
	}
	if got := stack.Extra["avatar_socket_data"]; got != hex.EncodeToString(socketData) {
		t.Fatalf("avatar socket vector lost: extra=%q want=%q (raw_entry=%x)",
			got, hex.EncodeToString(socketData), stack.RawEntry)
	}
	if len(refreshes) != 1 || refreshes[0].ListType != 1 || refreshes[0].SlotIndex != 0 {
		t.Fatalf("refreshes=%+v", refreshes)
	}
}

// 附件自己没有实例到期时间时（GM 面板邮件就是这种形状），领取必须按 dnfbridge 给出的
// 运行时 PVF 到期投影补上——当前客户端把"到期时间为 0"渲染成"已超过使用期限"，
// 背包里也会跟着显示过期。
func TestAddAttachmentToInventoryAppliesPVFExpirationWhenAttachmentHasNone(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}
	want := time.Date(2028, time.April, 4, 22, 0, 0, 0, time.UTC)
	resolution := alignedcmd.MailboxItemResolution{
		Kind:       "stackable",
		PVFPath:    "stackable/cash/chn_490701730.stk",
		SlotStart:  65,
		SlotEnd:    120,
		StackLimit: 1000,
		ExpireAt:   want,
		DefaultExtra: map[string]string{
			"expire_time":    "1838498400",
			"expire_unix":    "1838498400",
			"item_kind":      "stackable",
			"stackable_type": "[random reward item]",
		},
	}
	refreshes, err := addAttachmentToInventory(inventory, dnfrepo.MailAttachment{ItemID: 490701730, Count: 5}, resolution)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	slot, stack, ok := findClaimedTestStack(t, inventory, 490701730)
	if !ok {
		t.Fatalf("firecracker attachment did not land in the main list: %+v", inventory.Slots)
	}
	if !stack.ExpireAt.Equal(want) {
		t.Fatalf("claimed expiry = %s, want the runtime PVF deadline %s", stack.ExpireAt, want)
	}
	if stack.Extra["expire_time"] != "1838498400" || stack.Extra["item_kind"] != "stackable" {
		t.Fatalf("PVF expiration projection was not merged into the claimed stack: %+v", stack.Extra)
	}
	if len(refreshes) != 1 || refreshes[0].SlotIndex != slot {
		t.Fatalf("refreshes=%+v slot=%d", refreshes, slot)
	}
}

// 附件已经带了自己的实例到期时间时，绝不能拿 PVF 的日期去覆盖它。
func TestAddAttachmentToInventoryKeepsAttachmentExpirationOverPVF(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}
	instanceExpiry := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	resolution := alignedcmd.MailboxItemResolution{
		Kind:       "stackable",
		SlotStart:  65,
		SlotEnd:    120,
		StackLimit: 1000,
		ExpireAt:   time.Date(2028, time.April, 4, 22, 0, 0, 0, time.UTC),
		DefaultExtra: map[string]string{
			"expire_time": "1838498400",
		},
	}
	if _, err := addAttachmentToInventory(inventory, dnfrepo.MailAttachment{
		ItemID:   490701730,
		Count:    1,
		ExpireAt: instanceExpiry,
	}, resolution); err != nil {
		t.Fatalf("claim: %v", err)
	}
	stack, ok := inventory.Slots["0:65"]
	if !ok {
		// 主背包消耗品优先落 3..8 快捷格，按物品 ID 找回来即可。
		_, located, found := findClaimedTestStack(t, inventory, 490701730)
		if !found {
			t.Fatalf("claimed stack missing: %+v", inventory.Slots)
		}
		stack = located
	}
	if !stack.ExpireAt.Equal(instanceExpiry) {
		t.Fatalf("claimed expiry = %s, want the attachment's own %s", stack.ExpireAt, instanceExpiry)
	}
	if stack.Extra["expire_time"] != "1838498400" {
		t.Fatalf("missing-key merge should still fill expire_time: %+v", stack.Extra)
	}
}

// findClaimedTestStack 按物品 ID 找回刚领取的堆栈（消耗品会优先落 3..8 快捷格）。
func findClaimedTestStack(t *testing.T, inventory *dnfrepo.InventoryRecord, itemID int64) (int16, dnfrepo.ItemStack, bool) {
	t.Helper()
	for key, stack := range inventory.Slots {
		if stack.ItemID != itemID {
			continue
		}
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		slot, err := strconv.ParseInt(parts[1], 10, 16)
		if err != nil {
			continue
		}
		return int16(slot), stack, true
	}
	return 0, dnfrepo.ItemStack{}, false
}

// 解析器声明可自动进快捷栏的消耗品保持 3..8 优先，刷新仍然带 list 0。
func TestAddAttachmentToInventoryKeepsMainListBehaviour(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}
	resolution := alignedcmd.MailboxItemResolution{
		Kind:            "stackable",
		SlotStart:       65,
		SlotEnd:         120,
		StackLimit:      1000,
		PreferQuickSlot: true,
	}
	refreshes, err := addAttachmentToInventory(inventory, dnfrepo.MailAttachment{ItemID: 2683901, Count: 3}, resolution)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	stack, ok := inventory.Slots["0:3"]
	if !ok || stack.ItemID != 2683901 || stack.Count != 3 {
		t.Fatalf("main quick slot=%+v ok=%t", stack, ok)
	}
	if len(refreshes) != 1 || refreshes[0].ListType != dnfrepo.MainInventoryListType || refreshes[0].SlotIndex != 3 {
		t.Fatalf("refreshes=%+v", refreshes)
	}
}
