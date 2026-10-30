package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 2026-09-28 操作者报障（原文）："宠物打完附魔宝珠之后，重新穿上宠物时，大约 1 秒内
// 查看宠物看不到附魔属性；把宠物脱下来再看又正常。"
//
// 现场取证（国内服 192.0.2.11，`/opt/dnf90/logs/packet_log.txt` 10544–10870，
// 同一台机器的客户端 `90CN_trace.log` pid=47756）：
//
//  1. **那 1 秒是整容器重发**。同一批 op19 上，客户端 class1/op19 ACK →
//     class0/op2 mode1 的间隔：穿宠物/神器 **976–1047 ms**（8/8 次：00:44:16→17、
//     00:44:18→19、00:44:20→21、00:44:22→23、00:44:23→24、00:45:42→43、
//     00:45:47→48、00:45:53→55），而穿普通装备/时装只有 **41–94 ms**（12/12 次：
//     00:48:02…00:49:29）。普通装备那条链路默认
//     `DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed`，一个整容器都不发；
//     宠物/神器走的是 `PostActionRefreshSelectedItemContainers`（list 0/1/2/7/38，
//     现场 ≈24 KB / ≈194 行）。⇒ 多出来的 ≈0.95 s 就是这五个整容器。
//
//  2. **附魔只由 list 3 槽 26 的 op14 行带给客户端**。客户端补丁
//     `client-patch/90CN.cpp` 的 `ProxyPetItemUpdateDynamicState`（scope
//     `list7-slots0-139-or-list3-slot26-same-template-op14-native-dynamic-state`）
//     在原版 same-template op14 处理链上补一次 native dynamic-state setter；
//     dnfbridge 侧的对应契约写在 equipped_creature_item_refresh.go 的头注里。
//
//  3. **旧顺序把这条行排在整容器后面**：packet_log 里宠物 op19 的 post action 是
//     `refresh_selected_item_containers → refresh_selected_creature_state → appearance`，
//     于是客户端在 ACK 那一刻已经乐观穿上宠物（native sub_1D2DE80 搬 target-26
//     对象），却要等 ≈1 s 才收到带附魔的那条行。
//
// 修法（internal/modules/dnf/inventory/handler.go ownerAppliedPetEquipmentMoveResult）：
// 宠物/神器的 op19 **先发宠物投影、再发整容器**。包体、包数一个字节都没变
// （list 3 本来就不在整容器重发范围内）。本用例钉住"穿上宠物后客户端拿到的
// **第一个投影**就已经带附魔"，并且它必须排在五个整容器之前。
func TestPetEquipProjectionCarriesEnchantBeforeContainerReplay(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Name:        "Actor19",
		Job:         "15",
		Level:       90,
	}); err != nil {
		t.Fatal(err)
	}
	// 宠物背包（list 7 槽 0）里那只宠物：宝珠**不在**背包行上（现场的口径是
	// 宝珠先落在 dnf_pet_entry_extra，背包行只有宠物本体），穿上时由
	// equip.inheritPetEnchantFields 从 PetEntry 回填。
	bagRaw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(bagRaw[0:2], 0)
	binary.LittleEndian.PutUint32(bagRaw[2:6], 400990167)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"7:0": {
				ItemID:   400990167,
				Count:    1,
				RawEntry: bagRaw,
				Extra: map[string]string{
					"creature_serial_or_handle": "37",
					"item_kind":                 "equipment",
					"equipment_type":            "[creature]",
					"pvf_path":                  "equipment/creature/chn_2018_SpringFestival/chn_400990167.equ",
				},
			},
		},
		Warehouse: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "19",
		Entries: map[string]dnfrepo.PetEntry{
			"37": {
				PetKey:          "37",
				CreatureKey:     37,
				ItemID:          400990167,
				Name:            "Pet37",
				NameRaw:         []byte("Pet37"),
				Satiety:         72,
				Level:           7,
				Exp:             0x12345678,
				SourceListType:  7,
				SourceSlotIndex: 0,
				Extra: map[string]string{
					// 真源：附魔宝珠落在宠物条目上（dnf_pet_entry_extra）。
					"pet_enchant_card_item_id": "10008713",
					"enchant_card_id":          "10008713",
					"enchant_upgrade_count":    "0",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                            connection,
		connID:                          "pet-enchant-equip-ordering",
		accountID:                       "dnf:1",
		channel:                         channelcatalog.Channel{ID: 19},
		selectedCharacterID:             19,
		initialTownRouteCharacterID:     19,
		initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
		townSceneReadyCharacterID:       19,
		connectionTownActorOwnerChannel: 19,
		townActorOwnerChannel:           19,
		selectedCreatureStateTableSent:  true,
	}

	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMoveItemspace),
		// 客户端在背包端点写的是它读到的宠物句柄（现场 = 0x17E69FD8），
		// 这里用同一只宠物的序列号 37。
		petEquipOp19Body(0, 37),
	)
	if err != nil {
		t.Fatalf("handleAlignedGameCommand error = %v", err)
	}
	if !handled {
		t.Fatal("pet equip op19 was not handled")
	}

	// ① ACK 之后客户端拿到的**第一个投影**就是带附魔的 list 3 槽 26 行。
	ack, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("op19 ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	itemRefresh, rest := splitLongHengGameServerUpperPacket(t, rest)
	assertEquippedCreatureItemRefreshPacket(t, itemRefresh, 400990167, 37, 10008713, 0)

	// ② 宠物状态表与成长值紧随其后（同一个 post action）。
	state, rest := splitLongHengGameServerUpperPacket(t, rest)
	if state.Header.MsgID != currentCreatureStateTableMsgID || state.Header.Classification != 0 {
		t.Fatalf("op105 header=%+v body=%x", state.Header, state.Body)
	}
	growth, rest := splitLongHengGameServerUpperPacket(t, rest)
	if growth.Header.MsgID != currentCreatureGrowthMsgID || growth.Header.Classification != 0 {
		t.Fatalf("op102 header=%+v body=%x", growth.Header, growth.Body)
	}

	// ③ 只有在这之后才是宠物容器（list 7）的**变动槽**：
	//    DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH=slots（默认）下是一包 list 7 的
	//    op14 逐槽行（本次是"宠物离开的 7:0"⇒ 空行），**不回放任何整容器**；
	//    写 full 才回退到旧口径（list 0/1/2/7/38 五个整容器，现场 ≈23.9 KB / ≈0.95 s）。
	//    见 equip_move_refresh.go 的 petMoveContainerRefreshEnv。
	petRow, rest := splitLongHengGameServerUpperPacket(t, rest)
	if petRow.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) || petRow.Header.Classification != 0 {
		t.Fatalf("list-7 changed-slot row header=%+v body=%x", petRow.Header, petRow.Body)
	}
	if len(petRow.Body) != 3+currentItemListEntryWireSize || petRow.Body[0] != currentPetInventoryListType {
		t.Fatalf("list-7 changed-slot row body=%x", petRow.Body)
	}
	if count := binary.LittleEndian.Uint16(petRow.Body[1:3]); count != 1 {
		t.Fatalf("list-7 changed-slot row entry_count=%d, want 1", count)
	}
	if slot := binary.LittleEndian.Uint16(petRow.Body[3:5]); slot != 0 {
		t.Fatalf("list-7 changed-slot row slot=%d, want the vacated bag slot 0", slot)
	}
	if item := binary.LittleEndian.Uint32(petRow.Body[5:9]); item != ^uint32(0) {
		t.Fatalf("list-7 changed-slot row item=%#x, want the 0xFFFFFFFF empty row", item)
	}
	// 反向钉子：默认档一个整容器都不允许出现。
	assertNoCurrentSceneItemListPackets(t, rest)

	// ④ 最后才是 actor 外观重绑（默认档 class0/op2 **mode0** 当前场景对象自投影，
	//    带本地角色名 DSTR；mode0 才是客户端真正重挂本地 actor 外观的那条路）。
	appearance, trailing := splitLongHengGameServerUpperPacket(t, rest)
	if appearance.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || appearance.Header.Classification != 0 {
		t.Fatalf("mode0 header=%+v body=%x", appearance.Header, appearance.Body)
	}
	if !bytes.Contains(appearance.Body, append([]byte{7, 0, 0, 0}, []byte("Actor19")...)) {
		t.Fatalf("actor appearance packet is not the current-scene actor body: %x", appearance.Body)
	}
	for len(trailing) != 0 {
		// 战斗力量投影（class0/op413，私有 ABI）可能跟在最后，其余包一律不接受。
		extra, remaining := splitLongHengGameServerUpperPacket(t, trailing)
		if extra.Header.MsgID != 413 {
			t.Fatalf("unexpected packet after mode0: header=%+v body=%x", extra.Header, extra.Body)
		}
		trailing = remaining
	}

	// ⑤ 库里那次穿上确实带上了宝珠：穿戴行 43 字节布局的 0x10:0x14 = 宝珠 id。
	equipment, found, err := repositories.Equipment.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	worn, ok := equipment.Entries["26"]
	if !ok || worn.ItemID != 400990167 {
		t.Fatalf("worn slot26 entry = %+v", worn)
	}
	if got := binary.LittleEndian.Uint32(worn.RawEntry[0x10:0x14]); got != 10008713 {
		t.Fatalf("worn slot26 raw enchant = %d, want 10008713 (row=%x)", got, worn.RawEntry)
	}
	if worn.Extra["pet_enchant_card_item_id"] != "10008713" {
		t.Fatalf("worn slot26 extra = %+v", worn.Extra)
	}
}

// petEquipOp19Body 是现场 op19 "宠物栏 → 穿戴槽 26" 的 28 字节体
// （packet_log.txt:10736 `01 07 00 00 …` 逐字段同形）：
// src list 7 / slot 0 / 客户端读到的宠物句柄，dst list 3 / slot 26 / 实例 0，
// count=1、actor=-1、尾两字节 0。
func petEquipOp19Body(petSlot int16, serial uint32) []byte {
	body := make([]byte, 28)
	body[0] = 7
	binary.LittleEndian.PutUint16(body[1:3], uint16(petSlot))
	binary.LittleEndian.PutUint32(body[3:7], serial)
	binary.LittleEndian.PutUint32(body[7:11], 1)
	body[11] = 3
	binary.LittleEndian.PutUint16(body[12:14], 26)
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
	return body
}

// 反向钉子：整容器重发里**永远没有 list 3**（currentSelectedItemListTypes = {0,1,2,7,38}），
// 所以宠物附魔不可能靠它恢复；一旦有人把宠物投影挪回整容器之后，上面的第 ① 步就会
// 在客户端重新出现那 ≈1 s 的空窗。
func TestPetEquipContainerReplayNeverCarriesList3(t *testing.T) {
	for _, listType := range currentSelectedItemListTypes {
		if listType == currentSocketListEquipment {
			t.Fatalf("container replay list types = %v must not contain list 3", currentSelectedItemListTypes)
		}
	}
}

// assertNoCurrentSceneItemListPackets 回答"剩下的流里还有没有 class0/op13 整容器包"。
// 默认档（DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH=slots）下一个都不许有；这条断言是
// 第二轮修复的反向钉子：只要有人把宠物/神器的整容器回放加回来，它立刻变红。
func assertNoCurrentSceneItemListPackets(t *testing.T, data []byte) {
	t.Helper()
	rest := data
	for len(rest) != 0 {
		packet, remaining := splitLongHengGameServerUpperPacket(t, rest)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) && packet.Header.Classification == 0 {
			t.Fatalf("pet move still replayed a whole item container: body=%x", packet.Body)
		}
		rest = remaining
	}
}
