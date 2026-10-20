package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 2026-09-28 第二轮操作者报障（原文）："穿戴宠物 宠物正确出现 但是背包和已经装备的
// 宠物消失 重新选择角色后出现 和之前的装扮卡顿类似"。
//
// 现场（国内服 109.244.63.71 `/opt/dnf90/logs/packet_log.txt`，conn `game-000015`，
// 角色 1）：宠物 op19 是**唯一还在回放整容器**的搬运路径 —— class1/op19 ACK 之后
// `list_types=[0 1 2 7 38]`，现场 ≈23.9 KB（list 0 7621 B/64 行、list 1 12324 B/97 行、
// list 2 1434 B/12 行、list 7 2502 B/21 行、list 38 3 B），而落点 list 3 槽 26
// **从来不在这份名单里**（currentSelectedItemListTypes，见 current_item_list_types.go:17）。
// 同一会话里穿普通装备/时装（`DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed`）
// 一个整容器都不发，操作者验收"模型会跟着变"。
//
// 厂商（本版源码）在宠物搬运后**只发逐槽行**：
// `Network\Handlers\Pets\PetInventoryMoveCoordinator.cs:74-80` ⇒
// `SendUpdateItemList(Pet, PetCreatureRefreshSlots)`（+ list 3 的宠物槽），
// 槽集来自 `Network\Handlers\InventoryHandler.Move.cs:184-195` 的 changed slots，
// 空槽按 `Network\Builders\Inventory\ItemListUpdateBuilder.cs:81/:133-143` 发
// `slot + int32(-1)`。完整证据与三档取舍见 equip_move_refresh.go 的
// petMoveContainerRefreshEnv 长注。
//
// 本文件钉住三档的线格式：默认 slots（只发变动那一格、一个整容器都没有）、
// full（逐字节回退到五个整容器）、none（连那一格也不发）。
func TestPetMoveContainerRefreshDefaultSendsOnlyChangedList7Slot(t *testing.T) {
	service, session, conn := newPetMoveRefreshTestService(t, "")
	runPetMoveRefreshOp19(t, service, session, petMoveRefreshEquipBody(0, 37))

	ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) {
		t.Fatalf("op19 ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	// ① 宠物投影（list 3 槽 26 的 op14 权威行）。
	creatureRow, rest := splitLongHengGameServerUpperPacket(t, rest)
	if creatureRow.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("equipped creature row header=%+v", creatureRow.Header)
	}
	if slot := binary.LittleEndian.Uint16(creatureRow.Body[3:5]); slot != 26 {
		t.Fatalf("equipped creature row slot=%d, want 26", slot)
	}
	// ② 宠物状态表 + 成长值。
	for index := 0; index < 2; index++ {
		state, remaining := splitLongHengGameServerUpperPacket(t, rest)
		if state.Header.MsgID != currentCreatureStateTableMsgID && state.Header.MsgID != currentCreatureGrowthMsgID {
			t.Fatalf("creature projection #%d header=%+v", index, state.Header)
		}
		rest = remaining
	}
	// ③ 宠物容器（list 7）**逐槽**行：宠物离开的 7:0 ⇒ 空行（0xFFFFFFFF）。
	petRow, rest := splitLongHengGameServerUpperPacket(t, rest)
	if petRow.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) || petRow.Header.Classification != 0 {
		t.Fatalf("list-7 slot row header=%+v body=%x", petRow.Header, petRow.Body)
	}
	if petRow.Body[0] != currentPetInventoryListType ||
		binary.LittleEndian.Uint16(petRow.Body[1:3]) != 1 ||
		binary.LittleEndian.Uint16(petRow.Body[3:5]) != 0 ||
		binary.LittleEndian.Uint32(petRow.Body[5:9]) != ^uint32(0) {
		t.Fatalf("list-7 slot row body=%x, want one empty row at 7:0", petRow.Body)
	}
	// ④ 一个整容器都不许出现（这就是本轮修复本身）。
	assertNoCurrentSceneItemListPackets(t, rest)
}

// TestPetMoveContainerRefreshFullRollsBackToFiveContainers 钉住一键回退：
// DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH=full 必须逐字节回到 2026-09-28 第一轮 ——
// 宠物投影之后照样回放 list 0/1/2/7/38 五个整容器（现场 ≈23.9 KB）。
func TestPetMoveContainerRefreshFullRollsBackToFiveContainers(t *testing.T) {
	service, session, conn := newPetMoveRefreshTestService(t, petMoveContainerRefreshFull)
	if !service.petMoveContainerRefreshFullRequested() {
		t.Fatal("full did not select the legacy container replay")
	}
	runPetMoveRefreshOp19(t, service, session, petMoveRefreshEquipBody(0, 37))

	_, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	for index := 0; index < 3; index++ { // 宠物投影：list 3 槽 26 + op105 + op102
		_, remaining := splitLongHengGameServerUpperPacket(t, rest)
		rest = remaining
	}
	for index, wantListType := range currentSelectedItemListTypes {
		refresh, remaining := splitCurrentSceneItemListPacket(t, rest)
		if len(refresh.Body) == 0 || refresh.Body[0] != wantListType {
			t.Fatalf("container[%d] list=%d header=%+v body=%x", index, wantListType, refresh.Header, refresh.Body)
		}
		rest = remaining
	}
}

// TestPetMoveContainerRefreshNoneSendsNoPetContainerPacket 钉住最小回退档：
// DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH=none 连 list 7 的逐槽行也不发，
// 完全依赖客户端原生 op19 的对象搬运（与装扮档
// DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed 同口径）。
func TestPetMoveContainerRefreshNoneSendsNoPetContainerPacket(t *testing.T) {
	service, session, conn := newPetMoveRefreshTestService(t, petMoveContainerRefreshNone)
	if !service.petMoveContainerRefreshSuppressed() {
		t.Fatal("none did not select the suppressed tier")
	}
	runPetMoveRefreshOp19(t, service, session, petMoveRefreshEquipBody(0, 37))

	_, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	// 宠物投影（list 3 槽 26）仍在，list 7 一行都没有。
	creatureRow, rest := splitLongHengGameServerUpperPacket(t, rest)
	if creatureRow.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		binary.LittleEndian.Uint16(creatureRow.Body[3:5]) != 26 {
		t.Fatalf("equipped creature row header=%+v body=%x", creatureRow.Header, creatureRow.Body)
	}
	for len(rest) != 0 {
		packet, remaining := splitLongHengGameServerUpperPacket(t, rest)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketWalkoutPartyMember) && len(packet.Body) > 0 &&
			packet.Body[0] == currentPetInventoryListType {
			t.Fatalf("none still sent a list-7 packet: body=%x", packet.Body)
		}
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) {
			t.Fatalf("none still replayed a whole container: body=%x", packet.Body)
		}
		rest = remaining
	}
}

// TestPetMoveContainerRefreshNormalizesUnsetToSlots 钉住开关默认值语义：
// 未设置/未知取值 = slots（新口径），只有显式 full / none 才离开默认档。
func TestPetMoveContainerRefreshNormalizesUnsetToSlots(t *testing.T) {
	cases := map[string]string{
		"":           petMoveContainerRefreshSlots,
		"   ":        petMoveContainerRefreshSlots,
		"slots":      petMoveContainerRefreshSlots,
		"whatever":   petMoveContainerRefreshSlots,
		"full":       petMoveContainerRefreshFull,
		"legacy":     petMoveContainerRefreshFull,
		"none":       petMoveContainerRefreshNone,
		"suppressed": petMoveContainerRefreshNone,
		"OFF":        petMoveContainerRefreshNone,
		"FULL":       petMoveContainerRefreshFull,
	}
	for input, want := range cases {
		if got := normalizePetMoveContainerRefreshMode(input); got != want {
			t.Fatalf("normalizePetMoveContainerRefreshMode(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestEquipAppearanceCoalesceNormalizesUnsetToLeadingTrailing 钉住第二轮性能开关的
// 默认档：未设置/未知 = leading_trailing（新口径），只有显式 off 才回到
// "每次穿脱都同步发一个 mode0"。
func TestEquipAppearanceCoalesceNormalizesUnsetToLeadingTrailing(t *testing.T) {
	cases := map[string]string{
		"":                 equipAppearanceCoalesceLeadingTrailing,
		"leading_trailing": equipAppearanceCoalesceLeadingTrailing,
		"whatever":         equipAppearanceCoalesceLeadingTrailing,
		"off":              equipAppearanceCoalesceOff,
		"0":                equipAppearanceCoalesceOff,
		"legacy":           equipAppearanceCoalesceOff,
		"OFF":              equipAppearanceCoalesceOff,
	}
	for input, want := range cases {
		if got := normalizeEquipAppearanceCoalesceMode(input); got != want {
			t.Fatalf("normalizeEquipAppearanceCoalesceMode(%q) = %q, want %q", input, got, want)
		}
	}
	// 窗口：非法/越界一律回落到内置默认（0 ⇒ 200 ms）。
	// 上限 2026-09-28 从 2000 放宽到 5000：那条"连点合并不能长到'点完很久才看到'"
	// 的理由只成立于"窗口 == 尾随延迟"的年代；两者拆开后，"多久看到最终外观"由
	// DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS（上限 1000 ms）单独守，窗口只回答
	// "两次穿脱隔多久还算同一次连点"。所以 2001 现在**是合法值**，2001 以上的越界
	// 判据改由 5001 代表。
	for _, bad := range []string{"", "abc", "0", "-5", "5001", "999999999"} {
		if got := normalizeAvatarAppearanceCoalesceWindowMillis(bad); got != 0 {
			t.Fatalf("normalizeAvatarAppearanceCoalesceWindowMillis(%q) = %d, want 0 (default)", bad, got)
		}
	}
	if got := normalizeAvatarAppearanceCoalesceWindowMillis("120"); got != 120 {
		t.Fatalf("normalizeAvatarAppearanceCoalesceWindowMillis(120) = %d, want 120", got)
	}
	if got := normalizeAvatarAppearanceCoalesceWindowMillis("3000"); got != 3000 {
		t.Fatalf("normalizeAvatarAppearanceCoalesceWindowMillis(3000) = %d, want 3000", got)
	}
}

func newPetMoveRefreshTestService(t *testing.T, petMoveMode string) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Name:        "Actor19",
		Job:         "15",
		Level:       90,
	}); err != nil {
		t.Fatal(err)
	}
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
					"pvf_path":                  "equipment/creature/2018Summer/chn_400990167.equ",
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
				SourceListType:  currentPetInventoryListType,
				SourceSlotIndex: 0,
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
	switch normalizePetMoveContainerRefreshMode(petMoveMode) {
	case petMoveContainerRefreshFull:
		service.options.petMoveContainerRefreshFull = true
	case petMoveContainerRefreshNone:
		service.options.petMoveContainerRefreshNone = true
	}
	session := &gameSession{
		conn:                            connection,
		connID:                          "pet-move-refresh-" + petMoveMode,
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
	return service, session, connection
}

func runPetMoveRefreshOp19(t *testing.T, service *Service, session *gameSession, body []byte) {
	t.Helper()
	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMoveItemspace),
		body,
	)
	if err != nil {
		t.Fatalf("handleAlignedGameCommand error = %v", err)
	}
	if !handled {
		t.Fatal("pet equip op19 was not handled")
	}
}

// petMoveRefreshEquipBody 是现场 op19 "宠物栏 → 穿戴槽 26" 的 28 字节体。
func petMoveRefreshEquipBody(petSlot int16, serial uint32) []byte {
	body := make([]byte, 28)
	body[0] = currentPetInventoryListType
	binary.LittleEndian.PutUint16(body[1:3], uint16(petSlot))
	binary.LittleEndian.PutUint32(body[3:7], serial)
	binary.LittleEndian.PutUint32(body[7:11], 1)
	body[11] = 3
	binary.LittleEndian.PutUint16(body[12:14], 26)
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
	return body
}
