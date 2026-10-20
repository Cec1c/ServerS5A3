package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 穿脱（op19）"只发必要包"的回归钉子。
//
// 现场（2026-09-25 19:41，香港 packet_log.txt:2232-2277）× 客户端 trace
// （90CN_trace.log 同一分钟）证明每次穿/脱一件会发：
//
//	op19 ACK + class0/op13 ×5（整容器，≈10.1 KB）+ class0/op2 mode0（468B）+ op413
//
// 客户端对 op2 mode0 的反应是把本地 actor 的外挂外观重新同步（2026-09-28 的
// 客户端 trace 逐条对照：`mode0-owner-compare … branch=current-scene-create` →
// `local-actor-create` → `game-options-apply trigger=local-actor-create` →
// `town current actor repair … visual_synced=1`），而 mode1 只更新装备数据快照
// （`combat-power equipment snapshot … mode=1`）⇒ **mode1 档下模型必须重新选角才刷新**。
// 对整容器重发的反应则是把刚移动的那件从 list 0/1 里删掉，却永远收不到落点
// list 3 的权威行（"穿上了装备，身上没有、背包也不存在，重新选角后正常"）。
// 参考口径（厂商 9.16 的 `item_slot_refresh_count=0`、86JP Move.cs 一条物品栏刷新都不发）
// 与本批默认档一致：**ACK + 一次 op2 mode0 当前场景对象自投影**。
//
// 这三个用例把三代口径钉死，并顺带产出"改前/改后"的包数与字节数：
//
//	legacy     = 5 个整容器 + mode0（2026-09-25 之前）
//	oldDefault = 0 个容器 + mode1（2026-09-25..09-27，回退档
//	             DNFBRIDGE_EQUIP_APPEARANCE_REFRESH=mode1）
//	minimal    = 0 个容器 + mode0（本轮默认，≈468B）
func TestWearMoveEmitsOnlyAckAndMode0Appearance(t *testing.T) {
	type measurement struct {
		name      string
		packets   int
		bytes     int
		msgIDs    []uint16
		containsO int
	}
	run := func(t *testing.T, full, mode1Rollback bool) measurement {
		t.Helper()
		ctx := context.Background()
		repositories := dnfrepomemory.NewMemoryGroup()
		if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: "19",
			AccountID:   "dnf:1",
			Name:        "Actor19",
			Job:         "15",
			Level:       1,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
			CharacterID: "19",
			Entries:     map[string]dnfrepo.EquipmentEntry{},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots:       map[string]dnfrepo.ItemStack{},
			Warehouse:   map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}

		connection := &bufferConn{}
		service := &Service{
			options: options{
				accountID:                           "dnf:1",
				gameUpperHeader:                     gameUpperHeaderServer16,
				gameUpperBodyCodec:                  gameUpperBodyCodecPlain,
				equipMoveContainerRefreshFull:       full,
				equipAppearanceRefreshMode1Rollback: mode1Rollback,
			},
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		}
		session := &gameSession{
			conn:                            connection,
			connID:                          "wear-minimal-packets",
			channel:                         channelcatalog.Channel{ID: 19},
			selectedCharacterID:             19,
			initialTownRouteCharacterID:     19,
			initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
			townSceneReadyCharacterID:       19,
			connectionTownActorOwnerChannel: 19,
			townActorOwnerChannel:           19,
		}
		// 域侧（inventory.ownerAppliedEquipmentMoveResult）对一次"已提交的穿脱"给出的
		// 就是这两条后置动作：穿脱专用整容器动作 + 一次外观刷新。
		result := alignedcmd.Result{
			Operation: "move_itemspace",
			UpperResponses: []alignedcmd.UpperResponse{{
				MsgID:          uint16(dnfenum.CmdPacketMoveItemspace),
				Body:           []byte{1, 1, 31, 0, 1, 0, 0, 0, 3, 7, 0},
				Classification: dnfproto.DefaultChannelClassification,
				AllowCodec:     true,
			}},
			PostActions: []alignedcmd.PostAction{
				alignedcmd.PostActionRefreshSelectedWearContainers,
				alignedcmd.PostActionRefreshSelectedActorAppearance,
			},
		}
		if err := service.sendAlignedUpperResponses(session, result); err != nil {
			t.Fatalf("sendAlignedUpperResponses error = %v", err)
		}
		raw := connection.write.Bytes()
		out := measurement{bytes: len(raw)}

		rest := raw
		ack, rest := splitGameServerUpperPacket(t, rest)
		const ackCodecPrefix = 3
		// 传输层给每条 class1 业务体前置 3 字节 codec 前缀（与现场一致：
		// `000000` + `01 01 1f 00 …`）。
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) ||
			ack.Header.Classification != dnfproto.DefaultChannelClassification ||
			len(ack.Body) != ackCodecPrefix+11 ||
			!bytes.Equal(ack.Body[ackCodecPrefix:], []byte{1, 1, 31, 0, 1, 0, 0, 0, 3, 7, 0}) {
			t.Fatalf("first packet must be the op19 ACK: header=%+v body=%x", ack.Header, ack.Body)
		}
		out.packets = 1
		out.msgIDs = append(out.msgIDs, ack.Header.MsgID)
		for len(rest) > 0 {
			packet, remaining := splitCurrentGameServerUpperPacketAuto(t, rest)
			rest = remaining
			out.packets++
			out.msgIDs = append(out.msgIDs, packet.Header.MsgID)
			if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) {
				out.containsO++
				continue
			}
			if packet.Header.MsgID == 2 {
				const codecPrefix = 3
				if len(packet.Body) < codecPrefix+5 || !bytes.Equal(packet.Body[:codecPrefix], []byte{0, 0, 0}) {
					t.Fatalf("appearance packet body=%x", packet.Body)
				}
				wantMode := byte(0)
				if mode1Rollback {
					wantMode = 1
				}
				if packet.Body[codecPrefix] != wantMode {
					t.Fatalf("op2 mode byte = %d, want %d (body=%x)", packet.Body[codecPrefix], wantMode, packet.Body)
				}
			}
		}
		return out
	}

	legacy := run(t, true, false)
	if legacy.containsO != len(currentSelectedItemListTypes) {
		t.Fatalf("legacy rollback must replay %d containers, got %d (msg_ids=%v)",
			len(currentSelectedItemListTypes), legacy.containsO, legacy.msgIDs)
	}
	oldDefault := run(t, false, true)
	if oldDefault.containsO != 0 {
		t.Fatalf("the 2026-09-25 mode1 rollback must not replay any full container, got %d (msg_ids=%v)",
			oldDefault.containsO, oldDefault.msgIDs)
	}
	minimal := run(t, false, false)
	if minimal.containsO != 0 {
		t.Fatalf("default wear move must not replay any full container, got %d (msg_ids=%v)",
			minimal.containsO, minimal.msgIDs)
	}
	wantIDs := []uint16{uint16(dnfenum.CmdPacketMoveItemspace), 2}
	// 整容器重发档（legacy）在 ACK 与外观包之间夹着 5 个 op13 ⇒ 只钉首尾；
	// 两个 suppressed 档必须是 [op19 ACK, op2 外观] 两包。
	if legacy.msgIDs[0] != wantIDs[0] || legacy.msgIDs[len(legacy.msgIDs)-1] != wantIDs[1] {
		t.Fatalf("legacy wear move msg_ids=%v, want %v at both ends", legacy.msgIDs, wantIDs)
	}
	for _, measured := range []measurement{oldDefault, minimal} {
		if len(measured.msgIDs) < len(wantIDs) {
			t.Fatalf("wear move msg_ids=%v, want at least %v", measured.msgIDs, wantIDs)
		}
		for index, want := range wantIDs {
			if measured.msgIDs[index] != want {
				t.Fatalf("wear move msg_ids=%v, want prefix %v", measured.msgIDs, wantIDs)
			}
		}
	}
	// 默认档与 mode1 回退档都一个整容器都不发；只有 full 回退档才重发 5 个。
	for _, measured := range []measurement{oldDefault, minimal} {
		for _, id := range measured.msgIDs {
			if id == uint16(dnfenum.CmdPacketLeaveParty) {
				t.Fatalf("suppressed wear move still contains a class0/op13 container packet: %v", measured.msgIDs)
			}
		}
	}
	if minimal.bytes >= legacy.bytes {
		t.Fatalf("default wear move bytes=%d must be below legacy=%d", minimal.bytes, legacy.bytes)
	}
	// 注意：mode0 的角色体是**定长**的（≈468B 外观表 + 状态块），mode1 则随穿戴件数增长
	// （0 件 206B、24 件 1575B，线上实测）。所以"默认档比 mode1 档小"只在真实体量角色上
	// 成立 —— 那一条由 TestWearMoveAppearanceRefreshCostStaysOnTheActorProjection 用
	// 真实体量夹具钉住，这里只报数字。
	t.Logf("wear move packets/bytes: legacy(containers+mode0)=%d/%d %v -> mode1 rollback=%d/%d %v -> default(mode0)=%d/%d %v",
		legacy.packets, legacy.bytes, legacy.msgIDs,
		oldDefault.packets, oldDefault.bytes, oldDefault.msgIDs,
		minimal.packets, minimal.bytes, minimal.msgIDs)
}

// TestRealScriptPVFWearMoveKeepsBagAndWornStateConsistentAndSendsMinimalPackets
// 用**真实运行时 PVF** 的装备定义走一遍 op19 穿→脱，钉住两件事：
//  1. 背包与身上状态一致（穿完背包格空、装备槽有；脱完反过来）——这是域事务的性质，
//     与"客户端显示"无关；
//  2. 域侧对"已提交的穿脱"只给穿脱专用整容器动作 + 一次外观刷新，**没有**逐槽刷新行，
//     也没有通用整容器动作（后者已被 dnfbridge 默认档整段取消）。
//
// 运行方式：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<runtime>/data/dnf/Script.pvf \
//	  go test -buildvcs=false -count=1 -run TestRealScriptPVFWearMoveKeepsBagAndWornStateConsistent ./internal/services/dnfbridge/
func TestRealScriptPVFWearMoveKeepsBagAndWornStateConsistentAndSendsMinimalPackets(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	// 真实 PVF：取该职业的初始装备清单里第一件带落点的装备（含真实 pvf_path / 槽位）。
	entries, err := parseInitialCharacterEquipmentFromSource(archive, 15)
	if err != nil {
		t.Fatal(err)
	}
	var (
		itemID   int64
		wornSlot int16
		pvfRef   string
	)
	for _, entry := range entries {
		if entry.ItemID <= 0 || entry.Slot < 0 || entry.Slot > 30 {
			continue
		}
		itemID, wornSlot, pvfRef = entry.ItemID, entry.Slot, entry.PVFPath
		break
	}
	if itemID == 0 {
		t.Fatalf("real PVF job 15 initial equipment has no wearable entry: %+v", entries)
	}
	t.Logf("real PVF: item=%d worn_slot=%d pvf_path=%s", itemID, wornSlot, pvfRef)

	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const (
		characterID uint16 = 19
		bagSlot     int16  = 31
	)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Name:        "RealPVF19",
		Job:         "15",
		Level:       90,
		Stats:       map[string]int64{"gold": 0},
	}); err != nil {
		t.Fatal(err)
	}
	// 实例身份：op19 回传的 source identity 就是客户端读到的品质种子。真实 PVF 里
	// 这件装备没有耐久键，所以只种 quality_seed（与邮件领取端同一条重建口径）。
	const seed uint32 = 0x18C42E16
	if !itemquality.Valid(seed) {
		t.Fatalf("test seed %#x is not a valid instance seed", seed)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:" + strconv.Itoa(int(bagSlot)): {
				ItemID: itemID,
				Count:  1,
				Extra: map[string]string{
					"item_kind":    "equipment",
					"pvf_path":     pvfRef,
					"quality_seed": strconv.FormatUint(uint64(seed), 10),
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

	owner, err := dnfequip.NewOwnerWithPlacementValidator(repositories, dnfequip.PlacementValidatorFunc(
		func(context.Context, dnfequip.Placement) error { return nil },
	))
	if err != nil {
		t.Fatalf("equip owner: %v", err)
	}

	// --- 穿上：背包格必须空、装备槽必须有 ---
	equipMove, err := owner.Move(ctx, dnfequip.MoveCommand{
		SelectedCharacterID:      19,
		SourceListType:           dnfrepo.MainInventoryListType,
		SourceSlotIndex:          bagSlot,
		SourceInstanceValue:      int32(seed),
		MoveCount:                1,
		DestinationListType:      3,
		DestinationSlotIndex:     wornSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("real PVF equip move: %v", err)
	}
	if !equipMove.Changed || equipMove.Mode != "equip" {
		t.Fatalf("real PVF equip move result = %+v", equipMove)
	}
	equipResult := wearDomainResultForMove(t, equipMove, dnfrepo.MainInventoryListType, bagSlot, 3, wornSlot)
	requireRealPVFWearState(t, ctx, repositories, bagSlot, wornSlot, itemID, false)

	// --- 脱下：反过来 ---
	unequipMove, err := owner.Move(ctx, dnfequip.MoveCommand{
		SelectedCharacterID:      19,
		SourceListType:           dnfrepo.MainInventoryListType,
		SourceSlotIndex:          bagSlot,
		SourceInstanceValue:      0,
		MoveCount:                1,
		DestinationListType:      3,
		DestinationSlotIndex:     wornSlot,
		DestinationInstanceValue: int32(seed),
	})
	if err != nil {
		t.Fatalf("real PVF unequip move: %v", err)
	}
	if !unequipMove.Changed || unequipMove.Mode != "unequip" {
		t.Fatalf("real PVF unequip move result = %+v", unequipMove)
	}
	unequipResult := wearDomainResultForMove(t, unequipMove, dnfrepo.MainInventoryListType, bagSlot, 3, wornSlot)
	requireRealPVFWearState(t, ctx, repositories, bagSlot, wornSlot, itemID, true)

	// --- 下发：只有 op19 ACK + 一次 op2 mode1，没有 op13 整容器 ---
	for _, result := range []alignedcmd.Result{equipResult, unequipResult} {
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
			connID:                          "real-pvf-wear-minimal",
			channel:                         channelcatalog.Channel{ID: 19},
			selectedCharacterID:             characterID,
			initialTownRouteCharacterID:     characterID,
			initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
			townSceneReadyCharacterID:       characterID,
			connectionTownActorOwnerChannel: 19,
			townActorOwnerChannel:           19,
		}
		if err := service.sendAlignedUpperResponses(session, result); err != nil {
			t.Fatalf("sendAlignedUpperResponses: %v", err)
		}
		raw := connection.write.Bytes()
		rest := raw
		ack, rest := splitGameServerUpperPacket(t, rest)
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) {
			t.Fatalf("first packet header=%+v", ack.Header)
		}
		appearance, rest := splitGameServerUpperPacket(t, rest)
		const codecPrefix = 3
		const currentObjectKeyOffset = 0x4c
		// 默认档 = class0/op2 **mode0**（当前场景对象自投影）：mode 字节 0，
		// body[3]=route、body[4]=owner、body[0x4c:0x4e]=object key(=char id)。
		// 这才是客户端会重挂本地角色外观（visual_synced）的那条路。
		if appearance.Header.MsgID != 2 || appearance.Header.Classification != 0 ||
			len(appearance.Body) < codecPrefix+currentObjectKeyOffset+2 ||
			!bytes.Equal(appearance.Body[:codecPrefix], []byte{0, 0, 0}) ||
			appearance.Body[codecPrefix] != 0 ||
			appearance.Body[codecPrefix+3] != currentSceneObjectRoute ||
			binary.LittleEndian.Uint16(appearance.Body[codecPrefix+currentObjectKeyOffset:]) != characterID {
			t.Fatalf("appearance packet header=%+v body=%x", appearance.Header, appearance.Body)
		}
		for len(rest) > 0 {
			packet, remaining := splitCurrentGameServerUpperPacketAuto(t, rest)
			rest = remaining
			if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) {
				t.Fatalf("real PVF wear move still replayed a full container: %x", packet.Body)
			}
		}
	}
}

// wearDomainResultForMove 复刻 inventory 模块对"已提交穿脱"给出的 alignedcmd.Result
// （ACK + 穿脱专用整容器动作 + 一次外观刷新）。这里手写是为了不把 inventory 包拉进来
// 做跨包集成；域侧那份契约由 inventory 包的
// TestOwnerAppliedEquipmentMoveAlwaysRefreshesWearContainersThenAppearance 钉住。
func wearDomainResultForMove(t *testing.T, move dnfequip.MoveResult, sourceList byte, sourceSlot int16, destList byte, destSlot int16) alignedcmd.Result {
	t.Helper()
	ack := []byte{
		1,
		sourceList, byte(sourceSlot), byte(sourceSlot >> 8),
		1, 0, 0, 0,
		destList, byte(destSlot), byte(destSlot >> 8),
	}
	if move.SourceListType != sourceList || move.DestinationListType != destList {
		t.Fatalf("committed endpoints=(%d,%d)->(%d,%d), want (%d,%d)->(%d,%d)",
			move.SourceListType, move.SourceSlotIndex, move.DestinationListType, move.DestinationSlotIndex,
			sourceList, sourceSlot, destList, destSlot)
	}
	return alignedcmd.Result{
		Operation: "move_itemspace",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketMoveItemspace),
			Body:           ack,
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		},
	}
}

// requireRealPVFWearState 断言"背包与身上状态一致"：
// equipped=true（脱下之后）⇒ 背包格有这件、装备槽没有；否则相反。
func requireRealPVFWearState(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	bagSlot int16,
	wornSlot int16,
	itemID int64,
	equipped bool,
) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	bag, bagOccupied := inventory.Slots["0:"+strconv.Itoa(int(bagSlot))]
	equipment, found, err := repositories.Equipment.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	worn, wornOccupied := equipment.Entries[strconv.Itoa(int(wornSlot))]
	if equipped {
		if !bagOccupied || bag.ItemID != itemID {
			t.Fatalf("after unequip the bag slot must hold the item: %+v (all=%+v)", bag, inventory.Slots)
		}
		if wornOccupied {
			t.Fatalf("after unequip the worn slot must be empty: %+v", worn)
		}
		return
	}
	if bagOccupied {
		t.Fatalf("after equip the bag slot must be empty: %+v", bag)
	}
	if !wornOccupied || worn.ItemID != itemID {
		t.Fatalf("after equip the worn slot must hold the item: %+v (all=%+v)", worn, equipment.Entries)
	}
	if len(worn.RawEntry) != currentItemListEntryWireSize {
		t.Fatalf("worn raw entry size = %d, want 0x%X", len(worn.RawEntry), currentItemListEntryWireSize)
	}
	// 实例身份：客户端看到的品质种子必须原样落在那条 0x77 行里（"穿上后重登不掉"）。
	if got := binary.LittleEndian.Uint32(worn.RawEntry[6:10]); got == 0 {
		t.Fatalf("worn raw entry lost the instance identity: %x", worn.RawEntry)
	}
}
