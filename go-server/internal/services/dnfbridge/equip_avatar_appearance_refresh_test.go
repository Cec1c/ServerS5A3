package dnfbridge

// 穿脱时装（class1/op19 移动物品栏位置）后"客户端能不能**立刻**看到新外观"的回归钉子。
//
// 诊断（2026-09-28，完整证据与开关见 equip_move_refresh.go 头注释）：
//
//	客户端 trace（90CN_trace.log，2026-09-28 00:48，pid=47756，local_owner=0/11）逐条对照：
//	  · class0/op2 mode1（上一批的默认档）只走
//	    `town-copresence-dispatch opcode=2 mode=1` → `combat-power equipment snapshot … mode=1`
//	    —— 换的是**装备数据快照**，本地角色没有被重挂，所以模型不动；
//	  · class0/op2 mode0 才走
//	    `mode0-owner-compare … branch=current-scene-create` → `local-actor-create` →
//	    `game-options-apply trigger=local-actor-create` → `town current actor repair … visual_synced=1`
//	    —— 这才是外观真正刷新的那条路（登录时就是它把模型画对的）。
//
// 因此本文件钉三件事：
//  1. 穿上一件时装之后，下发的那一个 op2 必须是 **mode0**，且它的外观表在**这件时装的
//     槽位**上就是这件时装的 item id（客户端按外观行重挂部件）；
//  2. 脱下来之后同一个槽必须是**显式空行**（`currentActorMode0AppearanceEmptyItem`），
//     否则客户端会留着上一件的外观 —— "脱下不刷新"的老症状；
//  3. 每次都只有 `op19 ACK + op2 mode0` 两个包、且不重建任何整容器
//     （上一批取消的 5×class0/op13 一旦回来，操作者又会看到"身上没有、背包也没有"）。

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	// 真档时装：101530628 是 90CN 里的 [waist avatar]（现场 op19 `dst=(3,7)`）；
	// 这里用同一族的固定 id，不依赖任何外部资源。
	avatarAppearanceTestItemID int64 = 101530628
	// 装扮栏（list 1）槽位与穿戴槽位。穿戴槽 7 是 `[waist avatar]`
	// 在 33 槽 actor 装备表里的目标槽（equipment_placement.go:324-339 同一张表），
	// 同时也正是 mode0 外观表的第 7 行。
	avatarAppearanceTestAvatarSlot int16 = 31
	avatarAppearanceTestWornSlot   int16 = 7
)

// TestAvatarEquipAppearanceRefreshProjectsWornAvatarInMode0 是任务(a)：用真域事务
// （`equip.Owner.Move`）穿上一件时装，然后断言下发的那一个 op2 是 mode0，
// 且外观表的第 7 行就是这件时装的 item id；脱下后同一行必须回到显式空行。
func TestAvatarEquipAppearanceRefreshProjectsWornAvatarInMode0(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Name:        "Avatar19",
		Job:         "15",
		Level:       90,
	}); err != nil {
		t.Fatal(err)
	}
	// 0x77 行按客户端发来的形状写：slot 在 [0:2]、item id 在 [2:6]。
	avatarRaw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(avatarRaw[0:2], uint16(avatarAppearanceTestAvatarSlot))
	binary.LittleEndian.PutUint32(avatarRaw[2:6], uint32(avatarAppearanceTestItemID))
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			inventorySlotKeyAvatar(avatarAppearanceTestAvatarSlot): {
				ItemID:   avatarAppearanceTestItemID,
				Count:    1,
				RawEntry: avatarRaw,
				Extra: map[string]string{
					"item_kind": "avatar",
					"pvf_path":  "equipment/character/swordman/avatar/waist/waist.equ",
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

	owner, err := dnfequip.NewOwnerWithPlacementValidator(
		repositories,
		dnfequip.PlacementValidatorFunc(func(context.Context, dnfequip.Placement) error { return nil }),
	)
	if err != nil {
		t.Fatalf("equip owner: %v", err)
	}

	service, session, conn := newAvatarAppearanceRefreshTestService(t, repositories)

	// --- 穿上：装扮栏 1:31 → 穿戴槽 3:7 -------------------------------------
	equipMove, err := owner.Move(ctx, dnfequip.MoveCommand{
		SelectedCharacterID:  19,
		SourceListType:       1,
		SourceSlotIndex:      avatarAppearanceTestAvatarSlot,
		MoveCount:            1,
		DestinationListType:  3,
		DestinationSlotIndex: avatarAppearanceTestWornSlot,
	})
	if err != nil {
		t.Fatalf("equip avatar move: %v", err)
	}
	if !equipMove.Changed || equipMove.Mode != "equip" {
		t.Fatalf("equip avatar move result = %+v", equipMove)
	}
	worn, found, err := repositories.Equipment.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	wornEntry, equipped := worn.Entries[strconv.Itoa(int(avatarAppearanceTestWornSlot))]
	if !equipped || wornEntry.ItemID != avatarAppearanceTestItemID {
		t.Fatalf("worn avatar entry = %+v (all=%+v)", wornEntry, worn.Entries)
	}
	if _, stillInBag := mustLoadInventorySlots(t, ctx, repositories)["1:"+strconv.Itoa(int(avatarAppearanceTestAvatarSlot))]; stillInBag {
		t.Fatal("after equipping, the avatar inventory row must be gone")
	}

	if err := service.sendAlignedUpperResponses(session, avatarWearDomainResult(t, equipMove)); err != nil {
		t.Fatalf("sendAlignedUpperResponses(equip): %v", err)
	}
	assertAvatarWearEmitsAckThenMode0Appearance(t, conn, "Avatar19", avatarAppearanceTestWornSlot, uint32(avatarAppearanceTestItemID))

	// 客户端收到 mode0 之后必须能按外观行重挂部件：第 7 行就是这件时装，
	// 其余 13 行是显式空行（上面这个 helper 已经逐行断言）。

	// --- 脱下：穿戴槽 3:7 → 装扮栏 1:31 -------------------------------------
	// 2026-09-28 第二轮起 mode0 档有 leading + trailing 连点合并
	// （DNFBRIDGE_EQUIP_APPEARANCE_COALESCE，默认 200 ms）：同一窗口里的第二次
	// 穿脱不再同步发，而是并进窗口末尾的一次尾随刷新。本用例要分别断言两次
	// 动作各自的同步 mode0，所以在这里显式收一次合并窗口（等价于"玩家停手"），
	// 同时把待发尾随刷新清掉，避免它落到后面的断言里。
	service.stopAvatarAppearanceCoalesce(session, "avatar_wear_phase_boundary")
	unequipMove, err := owner.Move(ctx, dnfequip.MoveCommand{
		SelectedCharacterID:  19,
		SourceListType:       3,
		SourceSlotIndex:      avatarAppearanceTestWornSlot,
		MoveCount:            1,
		DestinationListType:  1,
		DestinationSlotIndex: avatarAppearanceTestAvatarSlot,
	})
	if err != nil {
		t.Fatalf("unequip avatar move: %v", err)
	}
	if !unequipMove.Changed || unequipMove.Mode != "unequip" {
		t.Fatalf("unequip avatar move result = %+v", unequipMove)
	}
	conn.write.Reset()
	if err := service.sendAlignedUpperResponses(session, avatarWearDomainResult(t, unequipMove)); err != nil {
		t.Fatalf("sendAlignedUpperResponses(unequip): %v", err)
	}
	assertAvatarWearEmitsAckThenMode0Appearance(t, conn, "Avatar19", avatarAppearanceTestWornSlot, currentActorMode0AppearanceEmptyItem)
}

// TestAvatarEquipAppearanceRefreshMode1RollbackStillAvailable 钉住一键回退：
// DNFBRIDGE_EQUIP_APPEARANCE_REFRESH=mode1 时同一个 post action 变回 mode1 穿戴态
// （客户端只更新装备数据快照、不重挂本地角色外观 ⇒ 该档下要重新选角才能看到新时装）。
func TestAvatarEquipAppearanceRefreshMode1RollbackStillAvailable(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Name:        "Avatar19",
		Job:         "15",
		Level:       90,
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
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries: map[string]dnfrepo.EquipmentEntry{
			strconv.Itoa(int(avatarAppearanceTestWornSlot)): {
				SlotIndex: avatarAppearanceTestWornSlot,
				ItemID:    avatarAppearanceTestItemID,
				RawEntry:  make([]byte, currentItemListEntryWireSize),
				Extra: map[string]string{
					"item_kind":                   "avatar",
					"current_exe_equipment_type":  strconv.Itoa(int(avatarAppearanceTestWornSlot)),
					"current_exe_runtime_move":    "1",
					"current_exe_inventory_count": "1",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	service, session, conn := newAvatarAppearanceRefreshTestService(t, repositories)
	service.options.equipAppearanceRefreshMode1Rollback = true

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
		t.Fatalf("sendAlignedUpperResponses(mode1 rollback): %v", err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) {
		t.Fatalf("first packet must stay the op19 ACK: %+v", ack.Header)
	}
	appearance, trailing := splitGameServerUpperPacket(t, rest)
	const codecPrefix = 3
	const mode1WornSlotRowOffset = 0x35
	if appearance.Header.MsgID != 2 || appearance.Header.Classification != 0 ||
		len(appearance.Body) <= codecPrefix+mode1WornSlotRowOffset+2 ||
		appearance.Body[codecPrefix] != 1 ||
		appearance.Body[codecPrefix+currentMode1CreateCountOffset] == 0 {
		t.Fatalf("mode1 rollback packet header=%+v body=%x", appearance.Header, appearance.Body)
	}
	if len(trailing) != 0 {
		t.Fatalf("mode1 rollback leaked trailing packets: %x", trailing)
	}
}

// TestWearMoveAppearanceRefreshCostStaysOnTheActorProjection 是任务(b)：
// 每次穿脱的**包集与仓储成本**必须只落在"本地 actor 自投影"上，不得重建任何整容器，
// 也不得打开任何物品事务。同一份真实体量夹具（45 行背包 / 32 行装扮栏 / 10 件已穿戴）
// 里再跑一次 `PostActionRefreshSelectedItemContainers` 作为标尺：默认档的读表次数、
// 事务数与字节数都必须严格低于它；默认档（mode0）也必须低于 mode1 回退档。
func TestWearMoveAppearanceRefreshCostStaysOnTheActorProjection(t *testing.T) {
	type cost struct {
		name                string
		packets             int
		bytes               int
		appearanceBodyBytes int
		containers          int
		appearances         int
		inventoryLoads      int
		accountInvLoads     int
		saves               int
		transactions        int
		equipmentLoads      int
	}
	run := func(t *testing.T, what string, mode1Rollback bool) cost {
		t.Helper()
		fixture := newRefreshCostFixture(t)
		fixture.service.options.equipAppearanceRefreshMode1Rollback = mode1Rollback
		fixture.warmUp(t)
		postActions := []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		}
		if what == "container_replay" {
			postActions = []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers}
		}
		result := alignedcmd.Result{
			Operation: "move_itemspace",
			UpperResponses: []alignedcmd.UpperResponse{{
				MsgID:          uint16(dnfenum.CmdPacketMoveItemspace),
				Body:           []byte{1, 31, 0, 1, 0, 0, 0, 3, 7, 0, 0},
				Classification: dnfproto.DefaultChannelClassification,
				AllowCodec:     true,
			}},
			PostActions: postActions,
		}
		if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
			t.Fatalf("%s: wear post action: %v", what, err)
		}
		measured := cost{
			name:            what,
			bytes:           fixture.buffer.write.Len(),
			inventoryLoads:  fixture.counter.total("Inventory.Load"),
			accountInvLoads: fixture.counter.total("AccountInventory.Load") + fixture.counter.total("AccountAssets.accountInventory.Load"),
			saves:           fixture.counter.total(".Save"),
			transactions: fixture.counter.total("AccountAssets.Begin") +
				fixture.counter.total("RentalAssets.Begin") +
				fixture.counter.total("CharacterItems.Begin"),
			equipmentLoads: fixture.counter.total("Equipment.Load"),
		}
		stream := fixture.buffer.write.Bytes()
		for len(stream) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
			stream = rest
			measured.packets++
			switch {
			case packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty):
				measured.containers++
			case packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && packet.Header.Classification == 0:
				measured.appearances++
				measured.appearanceBodyBytes = len(packet.Body)
				const codecPrefix = 3
				if what == "container_replay" {
					continue
				}
				wantMode := byte(0)
				if mode1Rollback {
					wantMode = 1
				}
				if len(packet.Body) <= codecPrefix || packet.Body[codecPrefix] != wantMode {
					t.Fatalf("%s: op2 mode byte=%x want=%d body=%x",
						measured.name, packet.Body, wantMode, packet.Body)
				}
			}
		}
		t.Logf("%s: packets=%d bytes=%d appearance_body_bytes=%d containers=%d appearances=%d equipment_loads=%d inventory_loads=%d account_inventory_loads=%d saves=%d transactions=%d | %s",
			measured.name, measured.packets, measured.bytes, measured.appearanceBodyBytes, measured.containers, measured.appearances,
			measured.equipmentLoads, measured.inventoryLoads, measured.accountInvLoads,
			measured.saves, measured.transactions, fixture.counter.summary())
		return measured
	}

	defaults := run(t, "default_mode0", false)
	rollback := run(t, "mode1_rollback", true)
	containerReplay := run(t, "container_replay", false)

	// 标尺自检：整容器档确实重发了 currentSelectedItemListTypes 个 class0/op13。
	if containerReplay.containers != len(currentSelectedItemListTypes) {
		t.Fatalf("container replay scale fixture emitted %d containers, want %d",
			containerReplay.containers, len(currentSelectedItemListTypes))
	}
	for _, measured := range []cost{defaults, rollback} {
		if measured.containers != 0 {
			t.Fatalf("%s rebuilt %d full class0/op13 containers; wear moves must not replay containers",
				measured.name, measured.containers)
		}
		if measured.appearances != 1 {
			t.Fatalf("%s emitted %d class0/op2 appearance packets, want exactly 1", measured.name, measured.appearances)
		}
		if measured.packets != 2 {
			t.Fatalf("%s emitted %d packets, want exactly op19 ACK + one appearance projection", measured.name, measured.packets)
		}
		// "不重建无关数据"：整容器重建会读背包/账号金库并跑修复性事务；穿脱外观
		// 投影一条都不该碰。
		if measured.inventoryLoads >= containerReplay.inventoryLoads {
			t.Fatalf("%s read the inventory %d times, not less than a full container replay (%d)",
				measured.name, measured.inventoryLoads, containerReplay.inventoryLoads)
		}
		if measured.accountInvLoads >= containerReplay.accountInvLoads ||
			measured.transactions != 0 || measured.saves != 0 {
			t.Fatalf("%s touched container state: account_inventory_loads=%d transactions=%d saves=%d (replay=%d/%d/%d)",
				measured.name, measured.accountInvLoads, measured.transactions, measured.saves,
				containerReplay.accountInvLoads, containerReplay.transactions, containerReplay.saves)
		}
		if measured.bytes >= containerReplay.bytes {
			t.Fatalf("%s bytes=%d must be below the full container replay=%d",
				measured.name, measured.bytes, containerReplay.bytes)
		}
		if measured.equipmentLoads > 6 {
			t.Fatalf("%s read the equipment record %d times, want the actor projection only",
				measured.name, measured.equipmentLoads)
		}
	}
	// mode0 的角色体是定长的；mode1 随穿戴件数增长 ⇒ 真实体量角色上默认档必须更小
	// （线上 1383..1575B vs 468B）。
	if defaults.bytes >= rollback.bytes {
		t.Fatalf("default mode0 projection bytes=%d must be below the mode1 rollback=%d",
			defaults.bytes, rollback.bytes)
	}
}

func newAvatarAppearanceRefreshTestService(t *testing.T, repositories dnfrepo.Group) (*Service, *gameSession, *bufferConn) {
	t.Helper()
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
		connID:                          "avatar-appearance-refresh",
		channel:                         channelcatalog.Channel{ID: 19},
		selectedCharacterID:             19,
		initialTownRouteCharacterID:     19,
		initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
		townSceneReadyCharacterID:       19,
		connectionTownActorOwnerChannel: 19,
		townActorOwnerChannel:           19,
	}
	if !currentTownSelfActorAppearanceReady(session) {
		t.Fatal("fixture session is not appearance-ready")
	}
	return service, session, connection
}

// avatarWearDomainResult 复刻 inventory 模块对"已提交穿脱"给出的 alignedcmd.Result
// （ACK + 穿脱专用整容器动作 + 一次外观刷新）。端点直接取域结果，避免把逆序/
// echo 判定写死在测试里。
func avatarWearDomainResult(t *testing.T, move dnfequip.MoveResult) alignedcmd.Result {
	t.Helper()
	return wearDomainResultForMove(
		t,
		move,
		move.SourceListType,
		move.SourceSlotIndex,
		move.DestinationListType,
		move.DestinationSlotIndex,
	)
}

// assertAvatarWearEmitsAckThenMode0Appearance 断言"ACK + 一个 mode0 自投影"两包，
// 且外观表在指定槽位上的 item id 就是期望值（其余 13 行必须是显式空行）。
func assertAvatarWearEmitsAckThenMode0Appearance(
	t *testing.T,
	conn *bufferConn,
	name string,
	wantSlot int16,
	wantItemID uint32,
) {
	t.Helper()
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("first packet must be the op19 ACK: %+v", ack.Header)
	}
	appearance, trailing := splitGameServerUpperPacket(t, rest)
	const codecPrefix = 3
	if appearance.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		appearance.Header.Classification != 0 ||
		len(appearance.Body) <= codecPrefix ||
		appearance.Body[codecPrefix] != 0 {
		t.Fatalf("the appearance projection must be class0/op2 mode0: header=%+v body=%x",
			appearance.Header, appearance.Body)
	}
	// 逐行：目标槽 = 这件时装的 item id，其余 13 个槽 = 显式空行
	// （assertCurrentTypedMode0FullAppearanceForTest 内部就是这么断言的）。
	assertCurrentTypedMode0FullAppearanceInContextForTest(
		t,
		appearance.Body[codecPrefix:],
		name,
		int(wantSlot),
		wantItemID,
		byte(19),
	)
	if len(trailing) != 0 {
		t.Fatalf("wear move leaked extra packets (containers or trailing): %x", trailing)
	}
}

func inventorySlotKeyAvatar(slot int16) string {
	return "1:" + strconv.Itoa(int(slot))
}

func mustLoadInventorySlots(t *testing.T, ctx context.Context, repositories dnfrepo.Group) map[string]dnfrepo.ItemStack {
	t.Helper()
	record, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return record.Slots
}
