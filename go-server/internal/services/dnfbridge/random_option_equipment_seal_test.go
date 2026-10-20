package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func randomOptionSealTestCatalog(t *testing.T, rarity, randomOption int, attach string) *pvfDungeonDropCatalog {
	t.Helper()
	source := bridgePVFSource{
		"monster/monster.lst": "", "stackable/stackable.lst": "",
		"equipment/equipment.lst":          "700 `random_option_seal.equ`\n",
		"equipment/random_option_seal.equ": fmt.Sprintf("[name]\n`魔封封装回归`\n[rarity]\n%d\n[random option]\n%d\n[attach type]\n`[%s]`\n[equipment type]\n`[weapon]`\n[durability]\n50\n", rarity, randomOption, attach),
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestRandomOptionEquipmentNewSealUsesPVF(t *testing.T) {
	for _, tc := range []struct {
		name, attach         string
		rarity, randomOption int
		want                 byte
	}{
		{"魔封紫装自由交易", "free", 2, 1, 0},
		{"魔封粉装自由交易", "free", 3, 1, 0},
		{"正魔封配置不覆盖自由交易", "FREE", 2, 2, 0},
		{"普通封装紫装", "sealing", 2, 0, 1},
		{"普通封装粉装", "sealing", 3, 0, 1},
		{"普通封装传说", "sealing", 6, 0, 1},
		{"普通白装", "free", 0, 0, 0},
		{"普通蓝装", "free", 1, 0, 0},
		{"非魔封自由紫装", "free", 2, 0, 0},
		{"非魔封自由粉装", "free", 3, 0, 0},
		{"自由传说不套魔封规则", "free", 6, 1, 0},
		{"不可交易模板", "trade", 2, 1, 0},
		{"账号绑定模板", "account", 3, 1, 0},
		{"缺交易配置", "", 2, 1, 0},
		{"非正魔封配置", "free", 2, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := randomOptionSealTestCatalog(t, tc.rarity, tc.randomOption, tc.attach)
			definition, err := catalog.ResolveItem(700)
			if err != nil {
				t.Fatal(err)
			}
			const seed = uint32(345678901)
			ground, err := currentDungeonDeathDropItemState(21, definition, 1, seed)
			if err != nil || ground.data[0x0D] != tc.want {
				t.Fatalf("PVF魔封新装备封装错误：实际%d 预期%d，错误%v", ground.data[0x0D], tc.want, err)
			}
			stack, err := buildCurrentDungeonPickupStack(9, definition, 1, seed)
			if err != nil {
				t.Fatal(err)
			}
			entry := currentItemListEntryFromStack(0, 9, stack)
			if entry.data[0x0D] != tc.want || len(stack.RawEntry) != currentItemListEntryWireSize || stack.RawEntry[0x0D] != tc.want || currentItemListBindFlag(false, stack.Extra) != tc.want {
				t.Fatal("新装备持久化与库存封装不一致")
			}
			if stack.Count != 1 || binary.LittleEndian.Uint32(entry.data[6:10]) != seed || binary.LittleEndian.Uint16(entry.data[11:13]) != 50 || entry.data[10] != 0 {
				t.Fatal("封装修复改变了数量、品质、耐久或强化")
			}
			if normalizeMagicBoxPVFType(tc.attach) == "free" {
				expectedBind, allowSealed, eligible := currentAuctionRegistrationPolicyForDefinition(definition, stack)
				if !eligible || expectedBind || allowSealed {
					t.Fatal("自由交易模板的新实例应能直接上架拍卖行")
				}
			}
		})
	}
}

func TestRandomOptionEquipmentRewardResolversKeepSeal(t *testing.T) {
	for _, tc := range []struct {
		name, attach         string
		rarity, randomOption int
		want                 bool
	}{
		{"魔封紫装自由交易", "free", 2, 1, false},
		{"魔封粉装自由交易", "free", 3, 1, false},
		{"普通自由紫装", "free", 2, 0, false},
		{"不可交易模板", "trade", 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := randomOptionSealTestCatalog(t, tc.rarity, tc.randomOption, tc.attach)
			box, err := resolveCurrentMagicBoxRewardItem(catalog, catalog.source, 700)
			if err != nil || box.Seal != tc.want {
				t.Fatalf("魔盒结果封装与新装备规则不一致：实际%t 预期%t 错误%v", box.Seal, tc.want, err)
			}
			rewards, err := resolveCurrentBoosterRewards(catalog, map[uint32]uint32{700: 1}, nil, dnfrepo.ItemStack{}, time.Now().UTC())
			if err != nil || len(rewards) != 1 || rewards[0].Seal != tc.want {
				t.Fatalf("礼盒奖励封装与新装备规则不一致：%+v 错误%v", rewards, err)
			}
			inventory := dnfrepo.InventoryRecord{}
			if _, err = grantCurrentBoosterReward(&inventory, nil, rewards[0]); err != nil {
				t.Fatal(err)
			}
			entry := currentItemListEntryFromStack(0, 9, inventory.Slots["0:9"])
			if (entry.data[0x0D] == 1) != tc.want {
				t.Fatal("礼盒元数据与实际入包封装不同")
			}
		})
	}
}

func TestRandomOptionSealPreservesExistingInstancesAndMailBoundaries(t *testing.T) {
	definition, err := randomOptionSealTestCatalog(t, 2, 1, "free").ResolveItem(700)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		mail       dnfrepo.MailRecord
		attachment dnfrepo.MailAttachment
		wantChange bool
	}{
		{"新系统自由交易模板", dnfrepo.MailRecord{Metadata: map[string]string{"system_mail": "true"}}, dnfrepo.MailAttachment{}, false},
		{"旧解封标记", dnfrepo.MailRecord{Metadata: map[string]string{"system_mail": "true"}}, dnfrepo.MailAttachment{Extra: map[string]string{"seal_flag": "0"}}, false},
		{"已有原始行", dnfrepo.MailRecord{Metadata: map[string]string{"system_mail": "true"}}, dnfrepo.MailAttachment{RawEntry: make([]byte, currentItemListEntryWireSize)}, false},
		{"首次穿戴已解封", dnfrepo.MailRecord{Metadata: map[string]string{"system_mail": "true"}}, dnfrepo.MailAttachment{Extra: map[string]string{"seal_removed_by_first_equip": "1"}}, false},
		{"玩家邮件", dnfrepo.MailRecord{SenderCharacterID: "20", Metadata: map[string]string{"system_mail": "true"}}, dnfrepo.MailAttachment{}, false},
		{"绑定附件", dnfrepo.MailRecord{Metadata: map[string]string{"system_mail": "true"}}, dnfrepo.MailAttachment{Bind: true}, false},
		{"未知来源", dnfrepo.MailRecord{}, dnfrepo.MailAttachment{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := append([]byte(nil), tc.attachment.RawEntry...)
			if got := initializeCurrentSystemMailEquipmentSeal(tc.mail, &tc.attachment, definition); got != tc.wantChange {
				t.Fatalf("邮件来源或旧实例边界错误：实际%t 预期%t", got, tc.wantChange)
			}
			if !bytes.Equal(raw, tc.attachment.RawEntry) || (tc.attachment.Extra["seal_flag"] == "1") != tc.wantChange {
				t.Fatal("旧附件被重新封装或新系统附件缺封装")
			}
		})
	}
	stored := dnfrepo.ItemStack{ItemID: 700, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize), Extra: map[string]string{"item_kind": "equipment", "seal_flag": "0", "seal_removed_by_first_equip": "1"}}
	if currentItemListEntryFromStack(0, 9, stored).data[0x0D] != 0 || stored.Extra["seal_flag"] != "0" {
		t.Fatal("库存投影把已解封魔封装备重新封装")
	}
}

func TestRealPVFRandomOptionCardRemainsFreeThroughGrantAndOverflow(t *testing.T) {
	catalog := realPackageSealCatalog(t)
	const itemID = uint32(100130169)
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatal(err)
	}
	document, err := parseCurrentRandomOptionDocument(catalog.source, definition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := document.Text("name")
	rarity, _ := document.Int("rarity")
	randomOption, _ := document.Int("random option")
	if name != "密制乌金护腿" || normalizeMagicBoxPVFType(definition.AttachType) != "free" || rarity != 2 || randomOption != 1 {
		t.Fatalf("现场紫装PVF定义不匹配：%s 品质%d 魔封%d 交易%s", name, rarity, randomOption, definition.AttachType)
	}
	for _, plainID := range []uint32{106040195, 100290006} {
		plain, err := catalog.ResolveItem(plainID)
		if err != nil || currentNewPVFEquipmentSealFlag(plain) != 0 {
			t.Fatalf("现场白蓝装被强制封装：物品%d 错误%v", plainID, err)
		}
	}
	testRealPVFCardEquipmentSealSurvivesEveryGrantAndOverflow(t, catalog, itemID, 0)
}
