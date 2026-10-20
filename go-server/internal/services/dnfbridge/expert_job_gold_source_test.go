package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场（香港 38.22.90.52，/opt/dnf90/logs/packet_log.txt 2026-09-25）：
//
//	:3505 RECV type=598 CREATE_EXPERT_JOB_STORE hex=...00 01000000 01 01000000 ed06 5c01 ffff
//	:3509 RECV type=599 ENTER_EXPERT_JOB_STORE  hex=...0300
//	:3513 RECV type=235 REQUEST_DISJOINT_ITEM   hex=...0300 0d00 00
//	:3516 SEND msg_id=235 plain_body_len=2      hex=...00 15        ← 失败码 21 = 金币不足
//	:3522 RECV type=600 CLOSE_EXPERT_JOB_STORE
//
// 这三条 RECV 体是本节全部用例的夹具来源（去掉 13 字节 legacy 头后的 body）。
const (
	// 现场 op598 体：Kind=0(分解机) / nameLen=1 / cost=1 / pos=(1773,348) / link=0xFFFF。
	currentExpertJobLiveStoreCreateBodyHex = "00010000000101000000ed065c01ffff"
	// 现场 op599 体：登记的门店主人 character_id=3。
	currentExpertJobLiveStoreEnterBodyHex = "0300"
	// 现场 op235 体：OwnerID=3 / TargetSlot=13 / TargetList=0（主背包）。
	currentExpertJobLiveStoreDisjointBodyHex = "03000d0000"
	// 现场 op235 失败回包体：失败标志 0 + 失败码 0x15=21。
	currentExpertJobLiveStoreDisjointFailureBodyHex = "0015"
)

// 香港库原文（/root/dbq.sh 'SELECT character_id, account_id, name, level, gold,
// expert_job_type, expert_job_exp FROM dnf_characters'）：
//
//	3  dnf:10001  test2  90  13364032  3  0
//
// 而 `SELECT ... FROM dnf_inventory_items WHERE entry_key='0:0'` 是 **Empty set**
// —— 金币只在 `dnf_characters.gold` 列，背包里从来没有 0:0 伪槽。
const (
	currentExpertJobLiveCharacterID        = "3"
	currentExpertJobLiveCharacterAccountID = "dnf:10001"
	currentExpertJobLiveCharacterGold      = int64(13364032)
)

func testHexBytes(t *testing.T, text string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// (a) 现场三条 RECV 体逐字段复核：手续费=1、门店主人=3、请求方=3（自身门店）。
// 这条把"夹具确实是操作者那一刻的报文"钉死，后面的金币判定才有意义。
func TestCurrentExpertJobLiveStoreBodiesPinSelfServiceDisjoint(t *testing.T) {
	create, err := parseCurrentExpertJobStoreCreateRequest(testHexBytes(t, currentExpertJobLiveStoreCreateBodyHex))
	if err != nil {
		t.Fatalf("live op598 body rejected: %v", err)
	}
	if create.Kind != currentExpertJobDisjointStoreKind || create.Cost != 1 {
		t.Fatalf("live op598 kind=%d cost=%d want kind=0 cost=1（截图「手续费：1金币」）", create.Kind, create.Cost)
	}
	if create.PositionX != 1773 || create.PositionY != 348 || create.OpaqueObjectLink != 0xFFFF {
		t.Fatalf("live op598 pos=(%d,%d) link=0x%04X", create.PositionX, create.PositionY, create.OpaqueObjectLink)
	}
	enter := testHexBytes(t, currentExpertJobLiveStoreEnterBodyHex)
	storeOwner := binary.LittleEndian.Uint16(enter[0:2])
	disjoint, err := parseCurrentExpertJobStoreDisjointRequest(testHexBytes(t, currentExpertJobLiveStoreDisjointBodyHex))
	if err != nil {
		t.Fatalf("live op235 body rejected: %v", err)
	}
	if disjoint.OwnerID != 3 || disjoint.TargetSlot != 13 || disjoint.TargetList != dnfrepo.MainInventoryListType {
		t.Fatalf("live op235 owner=%d slot=%d list=%d", disjoint.OwnerID, disjoint.TargetSlot, disjoint.TargetList)
	}
	if storeOwner != disjoint.OwnerID {
		t.Fatalf("live op599 owner=%d != op235 owner=%d", storeOwner, disjoint.OwnerID)
	}
	// 回包体 `00 15` 的码值必须仍然映射到 ErrInsufficientGold —— 这是把现场
	// "金币不足" 钉到金币分支（而不是 19/4/189/0xD4）的唯一依据。
	failure := testHexBytes(t, currentExpertJobLiveStoreDisjointFailureBodyHex)
	if failure[0] != 0 || failure[1] != currentExpertJobStoreError(dnfexpertjob.ErrInsufficientGold) {
		t.Fatalf("live failure body=%x, currentExpertJobStoreError(ErrInsufficientGold)=%d",
			failure, currentExpertJobStoreError(dnfexpertjob.ErrInsufficientGold))
	}
}

// (b) 【本缺陷的红→绿用例】金币读的是 `Character.Stats["gold"]`（= dnf_characters.gold），
// 不是背包伪槽 `0:0`。
//
// 现场形状：角色 3 有 13,364,032 金币、背包里**没有** `0:0` 这一行。
// 改动前 `currentExpertJobWalletGold` 读 `inventory.Slots["0:0"]` ⇒ `!ok` ⇒
// `ErrInsufficientGold` ⇒ 失败码 21（就是 :3516 的 `00 15`）。
func TestCurrentExpertJobGoldReadsCharacterGoldWithoutInventoryWalletSlot(t *testing.T) {
	service := &Service{}
	character := &dnfrepo.CharacterRecord{
		CharacterID: currentExpertJobLiveCharacterID,
		AccountID:   currentExpertJobLiveCharacterAccountID,
		Stats:       map[string]int64{"gold": currentExpertJobLiveCharacterGold, "expert_job_exp": 0},
	}
	// 现场背包：0:13 → 18400（dnf_inventory_items 原文），且**没有** 0:0。
	inventory := &dnfrepo.InventoryRecord{
		CharacterID: currentExpertJobLiveCharacterID,
		Slots:       map[string]dnfrepo.ItemStack{"0:13": {ItemID: 18400, Count: 1}},
	}
	gold, err := service.currentExpertJobWalletGold(character, inventory)
	if err != nil {
		t.Fatalf("角色金币必须从 Character.Stats 读到，got err=%v（改动前这里就是现场那次失败）", err)
	}
	if gold != currentExpertJobLiveCharacterGold {
		t.Fatalf("gold=%d want=%d", gold, currentExpertJobLiveCharacterGold)
	}
	// 写回也只写 Stats：不许再往背包里塞 0:0 伪槽。
	service.currentExpertJobSetWalletGold(character, inventory, gold-7)
	if character.Stats["gold"] != currentExpertJobLiveCharacterGold-7 {
		t.Fatalf("persisted gold=%d want=%d", character.Stats["gold"], currentExpertJobLiveCharacterGold-7)
	}
	if _, exists := inventory.Slots["0:0"]; exists {
		t.Fatalf("character_stats 档不得再创建背包 0:0 伪槽：%+v", inventory.Slots["0:0"])
	}
	// 没有任何 stats 的裸角色 = 0 金币，而不是"读不到钱包"的错误：
	// 余额够不够由调用方按实际手续费判定。
	empty, err := service.currentExpertJobWalletGold(&dnfrepo.CharacterRecord{CharacterID: "9"}, nil)
	if err != nil || empty != 0 {
		t.Fatalf("裸角色 gold=%d err=%v want 0/nil", empty, err)
	}
}

// (c) 同族但不同面的第二半：**自身门店不收费**，因此 0 金币也必须放行；
// 他人门店照旧收费、余额不足照旧失败。
func TestCurrentExpertJobSelfServiceDisjointChargesNoFee(t *testing.T) {
	newPair := func(gold int64) (*Service, *dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord) {
		service := &Service{}
		character := &dnfrepo.CharacterRecord{
			CharacterID: currentExpertJobLiveCharacterID,
			AccountID:   currentExpertJobLiveCharacterAccountID,
			Stats:       map[string]int64{"gold": gold},
		}
		inventory := &dnfrepo.InventoryRecord{
			CharacterID: currentExpertJobLiveCharacterID,
			Slots:       map[string]dnfrepo.ItemStack{"0:13": {ItemID: 18400, Count: 1}},
		}
		return service, character, inventory
	}

	// 自身门店 + 现场金币：成功、余额不动、不收手续费。
	service, character, inventory := newPair(currentExpertJobLiveCharacterGold)
	requesterGold, ownerGold, err := service.currentExpertJobSettleStoreFee(character, inventory, character, inventory, 1)
	if err != nil {
		t.Fatalf("自身门店分解必须放行，got err=%v（= 现场那次 00 15）", err)
	}
	if requesterGold != currentExpertJobLiveCharacterGold || ownerGold != currentExpertJobLiveCharacterGold {
		t.Fatalf("自身门店 requester=%d owner=%d want both=%d", requesterGold, ownerGold, currentExpertJobLiveCharacterGold)
	}
	if character.Stats["gold"] != currentExpertJobLiveCharacterGold {
		t.Fatalf("自身门店不得扣费：gold=%d", character.Stats["gold"])
	}

	// 自身门店 + 0 金币：也必须放行（改动前会被 "读不到钱包" 打死）。
	service, character, inventory = newPair(0)
	if _, _, err := service.currentExpertJobSettleStoreFee(character, inventory, character, inventory, 1); err != nil {
		t.Fatalf("0 金币的自身门店不得被手续费拦住，got err=%v", err)
	}

	// 他人门店：仍然收费、仍然会余额不足 —— 修复没有把别人的机器变成免费。
	service, requester, requesterInventory := newPair(5)
	_, owner, ownerInventory := newPair(1000)
	owner.CharacterID = "4"
	requesterGold, ownerGold, err = service.currentExpertJobSettleStoreFee(requester, requesterInventory, owner, ownerInventory, 1)
	if err != nil || requesterGold != 4 || ownerGold != 1001 {
		t.Fatalf("他人门店 requester=%d owner=%d err=%v want 4/1001/nil", requesterGold, ownerGold, err)
	}
	if requester.Stats["gold"] != 4 || owner.Stats["gold"] != 1001 {
		t.Fatalf("他人门店持久化 requester=%d owner=%d want 4/1001", requester.Stats["gold"], owner.Stats["gold"])
	}
	service, poor, poorInventory := newPair(0)
	_, other, otherInventory := newPair(1000)
	other.CharacterID = "4"
	if _, _, err := service.currentExpertJobSettleStoreFee(poor, poorInventory, other, otherInventory, 1); !errors.Is(err, dnfexpertjob.ErrInsufficientGold) {
		t.Fatalf("他人门店 0 金币必须仍判金币不足，got err=%v", err)
	}
	if poor.Stats["gold"] != 0 || other.Stats["gold"] != 1000 {
		t.Fatalf("被拒的手续费不得改动余额 requester=%d owner=%d", poor.Stats["gold"], other.Stats["gold"])
	}
}

// (e) 同族第二处现场：副职业**合成配方的金币消耗**（consumeCurrentExpertJobRecipe）。
// 改动前它同样直接读背包 `0:0`，所以在"金币只在 gold 列"的库里，任何
// `GoldCost > 0` 的配方都会回 errCurrentExpertJobInsufficientMaterials
// —— `currentExpertJobCompoundError` 把它映射成失败码 21，客户端弹的还是
// 「所持金币不足」。与分解机那条是同一个缺陷、同一串文案。
func TestCurrentExpertJobCompoundRecipeFeeReadsCharacterGold(t *testing.T) {
	materials := []dnfexpertjob.RecipeEntry{{ItemID: 2600149, Count: 2}}
	newFixture := func(gold int64) (*Service, *dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord) {
		service := &Service{}
		character := &dnfrepo.CharacterRecord{
			CharacterID: currentExpertJobLiveCharacterID,
			AccountID:   currentExpertJobLiveCharacterAccountID,
			Stats:       map[string]int64{"gold": gold},
		}
		inventory := &dnfrepo.InventoryRecord{
			CharacterID: currentExpertJobLiveCharacterID,
			Slots: map[string]dnfrepo.ItemStack{
				"0:13": {ItemID: 18400, Count: 1},
				"0:20": {ItemID: 2600149, Count: 2},
			},
		}
		return service, character, inventory
	}

	service, character, inventory := newFixture(currentExpertJobLiveCharacterGold)
	if err := service.consumeCurrentExpertJobRecipe(character, inventory, materials, 300); err != nil {
		t.Fatalf("合成手续费必须从 Character.Stats 扣，got err=%v（失败码 %d = 「金币不足」）",
			err, currentExpertJobCompoundError(err))
	}
	if character.Stats["gold"] != currentExpertJobLiveCharacterGold-300 {
		t.Fatalf("合成手续费未落到 Character.Stats：gold=%d", character.Stats["gold"])
	}
	if _, exists := inventory.Slots["0:20"]; exists {
		t.Fatalf("材料必须被消耗掉：%+v", inventory.Slots["0:20"])
	}

	// 余额不足：仍然拒绝，且对外码值仍是 21。
	service, poor, poorInventory := newFixture(10)
	err := service.consumeCurrentExpertJobRecipe(poor, poorInventory, materials, 300)
	if !errors.Is(err, errCurrentExpertJobInsufficientMaterials) || currentExpertJobCompoundError(err) != 21 {
		t.Fatalf("余额不足必须回 errCurrentExpertJobInsufficientMaterials/21，got err=%v code=%d", err, currentExpertJobCompoundError(err))
	}
	if poor.Stats["gold"] != 10 {
		t.Fatalf("被拒的合成不得扣钱：gold=%d", poor.Stats["gold"])
	}

	// 回退档：读背包 0:0，槽不存在 ⇒ 与改动前逐字节相同的拒绝。
	service = &Service{options: options{expertJobGoldSource: expertJobGoldInventoryWallet}}
	_, legacyCharacter, legacyInventory := newFixture(currentExpertJobLiveCharacterGold)
	if err := service.consumeCurrentExpertJobRecipe(legacyCharacter, legacyInventory, materials, 300); !errors.Is(err, errCurrentExpertJobInsufficientMaterials) {
		t.Fatalf("回退档必须复现改动前的拒绝，got err=%v", err)
	}
}

// (d) 一键回退档 `inventory_wallet` 必须**逐字节复现**现场那次失败：
// 读背包 0:0、槽不存在 ⇒ ErrInsufficientGold ⇒ 失败码 21 ⇒ 回包 `00 15`。
func TestCurrentExpertJobGoldInventoryWalletRollbackReproducesLiveFailure(t *testing.T) {
	if mode := readExpertJobGoldSourceMode(""); mode != expertJobGoldCharacterStats {
		t.Fatalf("默认档必须是 character_stats（修正后行为），got %s", mode)
	}
	for _, raw := range []string{"nonsense", "CHARACTER_STATS", " "} {
		if mode := readExpertJobGoldSourceMode(raw); mode != expertJobGoldCharacterStats {
			t.Fatalf("非法值 %q 必须落回 character_stats，got %s", raw, mode)
		}
	}
	if mode := readExpertJobGoldSourceMode(" Inventory_Wallet "); mode != expertJobGoldInventoryWallet {
		t.Fatalf("显式 inventory_wallet 必须回到改动前口径，got %s", mode)
	}

	service := &Service{options: options{expertJobGoldSource: expertJobGoldInventoryWallet}}
	character := &dnfrepo.CharacterRecord{
		CharacterID: currentExpertJobLiveCharacterID,
		Stats:       map[string]int64{"gold": currentExpertJobLiveCharacterGold},
	}
	inventory := &dnfrepo.InventoryRecord{
		CharacterID: currentExpertJobLiveCharacterID,
		Slots:       map[string]dnfrepo.ItemStack{"0:13": {ItemID: 18400, Count: 1}},
	}
	if _, _, err := service.currentExpertJobSettleStoreFee(character, inventory, character, inventory, 1); !errors.Is(err, dnfexpertjob.ErrInsufficientGold) {
		t.Fatalf("回退档必须复现 ErrInsufficientGold，got err=%v", err)
	}
	if code := currentExpertJobStoreError(dnfexpertjob.ErrInsufficientGold); code != testHexBytes(t, currentExpertJobLiveStoreDisjointFailureBodyHex)[1] {
		t.Fatalf("回退档失败码=%d，与现场回包 %s 不符", code, currentExpertJobLiveStoreDisjointFailureBodyHex)
	}
	if _, err := service.currentExpertJobWalletGold(character, inventory); !errors.Is(err, dnfexpertjob.ErrInsufficientGold) {
		t.Fatalf("回退档读金币必须回 ErrInsufficientGold，got %v", err)
	}
}
