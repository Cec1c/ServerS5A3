package dnfbridge

// 原段88（选择角色后武器锻造效果消失、穿脱后恢复）的第16轮有界回归。
//
// 做法：把**一个持久化在内存仓储里的锻造等级**同时穿过选角与穿脱两条链路上的真实发送入口，
// 核对同一个持久化字节在几条链路上都被下发、且互相一致。
//
// 本轮调用的入口（都是生产入口，不是测试专用造行函数）：
//
//	sendCurrentSelectInventoryBootstrap  —— 选角引导的容器下发（list 3 装备栏是第七个容器）
//	sendSelectedSceneUserInfo23Mode1Only  —— 选角链路里 mode1 的发送入口（从线路包读创建行）
//	reassertLoginEquipmentTailRows       —— 选角容器之后既有的 op14 逐槽尾部重发（与穿脱同形）
//	sendLoginEquipmentWornSlotRow        —— 单槽装备收敛的 op14 逐槽行（读装备仓储权威行）
//
// **明确缺口（据实标注，不用更强口径）**：
//   - 没有调用 handleOnlineSelectCharacter 那一层整条封装（需要完整会话/账号装配），只调用选角链上
//     这两个真实发送入口；穿脱侧只覆盖逐槽 op14 载体，没有驱动 op19 移动命令本身。
//   - mode1 按服务夹具配置的16字节头解析，严格要求实际发送一条装备创建行并检查+33。
//     子代理初稿用混合流auto helper按13字节头误读出0行，父已修正解析并删除构包回退。
//   - 因此本文件只证明【所测入口 + 本 fixture 下，同一落库数据经这几条下行投影时锻造字节都正确且一致】，
//     不排除完整 handle 选角顺序、真实角色库存/账号装配或其它代码路径带来的差异。
//
// 夹具边界（**本轮是已知 ID + 合成行，不是真实武器夹具**）：夹具用 dnfrepomemory 内存仓储与**合成**
// 0x77 行 + 合成 Extra；本轮**没有**打开固定 PVF，也没有读任何历史 packet_log 或现场库行，
// 因此不能把历史“现场角色 6 的槽 12”当作本轮读取证明；选主武器槽 12 与件号 101040493 只是取一个
// 已知合法的槽位与件号（该件号的 [weapon]/[durability] 来自既有真档用例的说明，不是本轮读取）。
// 未连 MySQL、未访问玩家库、未跑客户端进程、未截图。
//
// 行偏移只用本仓已有证据，不猜：
//
//   - 0x77 物品行 +0x57 = 锻造等级（持久化落点）。证据：Docs/D33_FORGING_20261014.md 第 3 节、
//     modules/dnf/inventory/owner_separate_upgrade.go 写侧、以及既有
//     login_equipment_tail_reassert_test.go 与 TestEquipmentProjectionReadsForgeFromBothExtraAliases。
//   - mode1 装备创建行 +33 = state120。证据：userinfo_mode1_forge_state120_regression_test.go
//     钉死的当前 EXE 创建行契约（forgeOffset = 33）。本文件不重复断言该契约本身，
//     只在主用例里断言创建行长度等于生产基长 currentMode1EquipmentCreateRowBaseWireSize：
//     可选块一旦让行变宽，+33 会立刻指向别的字段，断言当场失败而不是静默漂移。
//
// native 读法：客户端到底在哪个坐标读锻造等级（创建行的 state120 还是 0x77 行的 0x57）**本轮未核**
//（该项可在磁盘上离线核对当前 EXE，本轮没有做，也没有读客户端进程）。生产因此保留 op14 尾部重发
//（login_equipment_tail_reassert.go）作为兜底。

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	// forgingSelectRound16WeaponItemID 是既有真档用例用过的已知装备件号
	// （101040493，[weapon] / [durability] 48），本用例只把它当合法件号用。
	forgingSelectRound16WeaponItemID = 101040493
	// forgingSelectRound16WeaponSlot 是**合法主武器槽 12**（本轮夹具行是合成的）。
	forgingSelectRound16WeaponSlot = 12
	// forgingSelectRound16ForgeLevel 是本轮持久化的锻造等级，取 5 以便与历史夹具的 4/2 区分，
	// 从而证明下发值来自持久化字段而不是任何夹具常量。
	forgingSelectRound16ForgeLevel = 5
	// forgingSelectRound16CharacterID 只是内存夹具的角色号。
	forgingSelectRound16CharacterID  = 6
	forgingSelectRound16InstanceSeed = 346360515

	// forgingSelectRound16Mode1State120Offset = mode1 创建行 +33 = state120，证据见文件头。
	forgingSelectRound16Mode1State120Offset = 33
)

// seedForgingSelectRound16Character 建出**内存合成夹具**：角色记录、空背包（仓库也空），
// 以及一条合法主武器槽 12 的合成 0x77 行。锻造等级只写在持久化 Extra byte_57/value_57 上，
// 合成行的 0x57 保持 0 —— 这正是【持久化字段是不是投影源】的判据。
func seedForgingSelectRound16Character(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: defaultAccountPrefix + "10011",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: strconv.Itoa(forgingSelectRound16CharacterID),
		AccountID:   defaultAccountPrefix + "10011",
		Name:        "forging16",
		Job:         "11",
		Level:       90,
		Stats:       map[string]int64{"gold": 1000000, dnfrepo.CharacterGrowTypeStatKey: 4},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: strconv.Itoa(forgingSelectRound16CharacterID),
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0x00:0x02], uint16(forgingSelectRound16WeaponSlot))
	binary.LittleEndian.PutUint32(raw[0x02:0x06], uint32(forgingSelectRound16WeaponItemID))
	binary.LittleEndian.PutUint32(raw[0x06:0x0A], forgingSelectRound16InstanceSeed)
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 48)
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: strconv.Itoa(forgingSelectRound16CharacterID),
		Entries: map[string]dnfrepo.EquipmentEntry{
			strconv.Itoa(forgingSelectRound16WeaponSlot): {
				SlotIndex: forgingSelectRound16WeaponSlot,
				ItemID:    forgingSelectRound16WeaponItemID,
				RawEntry:  raw,
				Extra: map[string]string{
					"byte_57":                     strconv.Itoa(forgingSelectRound16ForgeLevel),
					"value_57":                    strconv.Itoa(forgingSelectRound16ForgeLevel),
					"current_exe_equipment_type":  strconv.Itoa(forgingSelectRound16WeaponSlot),
					"current_exe_runtime_move":    "1",
					"current_exe_inventory_count": "1",
					"quality_seed":                strconv.Itoa(forgingSelectRound16InstanceSeed),
					"item_kind":                   "equipment",
					"equipment_type":              "[weapon]",
					"durability":                  "48",
					"max_durability":              "48",
					"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// forgingSelectRound16SingleList3Row 从既有容器/增量体里取出**唯一**那条 list 3 行。
// 只有一行是刻意的：夹具只有一个已穿戴槽，多出来的行说明投影不是我们钉的那条。
func forgingSelectRound16SingleList3Row(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 3 || body[0] != currentSocketListEquipment {
		t.Fatalf("body = %x, want a list %d body", body, currentSocketListEquipment)
	}
	if count := int(binary.LittleEndian.Uint16(body[1:3])); count != 1 {
		t.Fatalf("list-3 row count = %d, want exactly the one persisted weapon", count)
	}
	if len(body) != 3+currentItemListEntryWireSize {
		t.Fatalf("list-3 body_len = %d, want %d", len(body), 3+currentItemListEntryWireSize)
	}
	return body[3:]
}

// forgingSelectRound16SingleOp14Row 取单包 op14（msg = CmdPacketWalkoutPartyMember，class 0）的
// 唯一 list 3 行 —— 这是【穿脱】逐槽下发的既有载体形状。
func forgingSelectRound16SingleOp14Row(t *testing.T, stream []byte) []byte {
	t.Helper()
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
	if len(rest) != 0 {
		t.Fatalf("trailing packets after the op14 carrier: %x", rest)
	}
	if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("carrier = class %d msg %d, want class 0 msg %d",
			packet.Header.Classification, packet.Header.MsgID, uint16(dnfenum.CmdPacketWalkoutPartyMember))
	}
	return forgingSelectRound16SingleList3Row(t, packet.Body)
}

func forgingSelectRound16AssertWeaponRow(t *testing.T, row []byte, source string) {
	t.Helper()
	if got := int(binary.LittleEndian.Uint16(row[0x00:0x02])); got != forgingSelectRound16WeaponSlot {
		t.Fatalf("%s: row slot = %d, want %d", source, got, forgingSelectRound16WeaponSlot)
	}
	if got := binary.LittleEndian.Uint32(row[0x02:0x06]); got != uint32(forgingSelectRound16WeaponItemID) {
		t.Fatalf("%s: row item = %d, want %d", source, got, forgingSelectRound16WeaponItemID)
	}
}

// TestForgingSelectRound16PersistedForgeReachesSelectAndWearProjections 是本轮主用例：
// 同一个**持久化在内存仓储里**的锻造等级必须同时出现在
//
//	(1) 选角引导容器 list 3 的行（0x77 行 +0x57）—— 入口 sendCurrentSelectInventoryBootstrap
//	(2) 选角 mode1 的装备创建行（创建行 +33 = state120）—— 先试 sendSelectedSceneUserInfo23Mode1Only
//	    的真实线路包，取不到创建行分支时据实记录缺口，再走同一份生产构包入口
//	(3) 选角容器之后既有 op14 逐槽尾部重发的行（0x77 行 +0x57）—— 入口 reassertLoginEquipmentTailRows
//	(4) 单槽装备收敛的逐槽 op14 行（0x77 行 +0x57）—— 入口 sendLoginEquipmentWornSlotRow
//
// 四条**真实发送入口**上，而不是任何一条【造行 helper】的形状上。
func TestForgingSelectRound16PersistedForgeReachesSelectAndWearProjections(t *testing.T) {
	// 默认档：op14 尾部重发生效（写 off 的只是回退字面量）。
	t.Setenv(loginEquipmentTailReassertEnv, "")
	t.Setenv("DNFBRIDGE_MODE1_EQUIPMENT_RAW_BLOCKS", "evidence")

	repositories := dnfrepomemory.NewMemoryGroup()
	seedForgingSelectRound16Character(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	service.characterStats = testCharacterStatTable(t)

	// (1) 选角引导：实际发送入口把 list 3（装备栏）作为第七个容器发出去。
	selectConnection := &bufferConn{}
	selectSession := newLoginEquipmentTestSession(selectConnection, forgingSelectRound16CharacterID)
	if err := service.sendCurrentSelectInventoryBootstrap(selectSession, "round16_select"); err != nil {
		t.Fatalf("select-character bootstrap: %v", err)
	}
	bodies, rest := splitLoginBootstrapContainers(t, selectConnection.write.Bytes())
	requireCurrentLoginBootstrapTailPackets(t, rest)
	equipmentBody, ok := bodies[currentSocketListEquipment]
	if !ok {
		t.Fatalf("select-character bootstrap never sent list %d; containers=%v", currentSocketListEquipment, bodies)
	}
	containerRow := forgingSelectRound16SingleList3Row(t, equipmentBody)
	forgingSelectRound16AssertWeaponRow(t, containerRow, "select container row")

	// (2) 选角 mode1：先走链路真实发送入口，从线路包上看它到底发了什么。
	mode1Connection := &bufferConn{}
	mode1Session := newLoginEquipmentTestSession(mode1Connection, forgingSelectRound16CharacterID)
	if err := service.sendSelectedSceneUserInfo23Mode1Only(mode1Session, "round16_select_mode1"); err != nil {
		t.Fatalf("select-character mode1 send entry: %v", err)
	}
	mode1Packet, mode1Rest := splitLongHengGameServerUpperPacket(t, mode1Connection.write.Bytes())
	if len(mode1Rest) != 0 {
		t.Fatalf("trailing packets after the mode1 carrier: %x", mode1Rest)
	}
	if mode1Packet.Header.Classification != 0 || mode1Packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("mode1 carrier = class %d msg %d, want class 0 msg %d",
			mode1Packet.Header.Classification, mode1Packet.Header.MsgID, uint16(dnfenum.CmdPacketSetUDPIPPort))
	}
	// The service explicitly uses a 16-byte header. The mixed-stream auto
	// helper defaults op2 to 13 bytes; using it here misreads three header
	// bytes as payload and falsely reports zero equipment rows.
	mode1 := mode1Packet.Body
	if len(mode1) != currentMode1BaseWireSize+currentMode1EquipmentCreateRowBaseWireSize || int(mode1[currentMode1CreateCountOffset]) != 1 {
		t.Fatalf("mode1 did not send exactly one complete weapon creation row: len=%d count=%d", len(mode1), mode1[currentMode1CreateCountOffset])
	}
	mode1Row := mode1[currentMode1CreateRowsOffset : currentMode1CreateRowsOffset+currentMode1EquipmentCreateRowBaseWireSize]
	if int(mode1Row[0]) != forgingSelectRound16WeaponSlot || binary.LittleEndian.Uint32(mode1Row[1:5]) != uint32(forgingSelectRound16WeaponItemID) {
		t.Fatalf("mode1 sent another equipment identity: %x", mode1Row)
	}
	mode1Forge := mode1Row[forgingSelectRound16Mode1State120Offset]

	// (3) 选角容器之后既有的 op14 逐槽尾部重发（与穿脱同形）。
	reassertConnection := &bufferConn{}
	reassertSession := newLoginEquipmentTestSession(reassertConnection, forgingSelectRound16CharacterID)
	service.reassertLoginEquipmentTailRows(reassertSession, "round16_wear_reassert")
	reassertRow := forgingSelectRound16SingleOp14Row(t, reassertConnection.write.Bytes())
	forgingSelectRound16AssertWeaponRow(t, reassertRow, "op14 tail reassert row")

	// (4) 单槽装备收敛路径（同一份 op14 载体，读装备仓储的权威行）。
	slotConnection := &bufferConn{}
	slotSession := newLoginEquipmentTestSession(slotConnection, forgingSelectRound16CharacterID)
	if err := service.sendLoginEquipmentWornSlotRow(slotSession, forgingSelectRound16WeaponSlot, "round16_wear_slot"); err != nil {
		t.Fatalf("worn slot op14 carrier: %v", err)
	}
	slotRow := forgingSelectRound16SingleOp14Row(t, slotConnection.write.Bytes())
	forgingSelectRound16AssertWeaponRow(t, slotRow, "op14 worn slot row")

	// 断言：持久化字段（byte_57/value_57 = 5）就是这几条链路的共同来源。
	if got := containerRow[loginEquipmentTailBlockFirstByte]; got != forgingSelectRound16ForgeLevel {
		t.Fatalf("select container forge(+0x57) = %d, want %d (row=%x)", got, forgingSelectRound16ForgeLevel, containerRow)
	}
	if mode1Forge != forgingSelectRound16ForgeLevel {
		t.Fatalf("select mode1 state120(+%d) = %d, want %d (row=%x)",
			forgingSelectRound16Mode1State120Offset, mode1Forge, forgingSelectRound16ForgeLevel, mode1Row)
	}
	if got := reassertRow[loginEquipmentTailBlockFirstByte]; got != forgingSelectRound16ForgeLevel {
		t.Fatalf("wear op14 reassert forge(+0x57) = %d, want %d (row=%x)", got, forgingSelectRound16ForgeLevel, reassertRow)
	}
	if got := slotRow[loginEquipmentTailBlockFirstByte]; got != forgingSelectRound16ForgeLevel {
		t.Fatalf("wear op14 worn-slot forge(+0x57) = %d, want %d (row=%x)", got, forgingSelectRound16ForgeLevel, slotRow)
	}
	if containerRow[loginEquipmentTailBlockFirstByte] != mode1Forge ||
		reassertRow[loginEquipmentTailBlockFirstByte] != mode1Forge ||
		slotRow[loginEquipmentTailBlockFirstByte] != mode1Forge {
		t.Fatalf("select/wear projections disagree: container=%d mode1=%d reassert=%d worn=%d",
			containerRow[loginEquipmentTailBlockFirstByte], mode1Forge,
			reassertRow[loginEquipmentTailBlockFirstByte], slotRow[loginEquipmentTailBlockFirstByte])
	}
}

// TestForgingSelectRound16ForgeCarrierIsTheRawRowAndTheAliases 说明**同一落库数据的三种载体形状**
// 经实际穿脱侧逐槽入口（sendLoginEquipmentWornSlotRow）时都到达线路：投影以持久化 0x77 行为基底
// （setByte 把 0 当 no-op），Extra 的 byte_57 / value_57 任一非零时覆盖它。
// 夹具同样是**合成内存行**：逐例只在内存仓储里改一个载体字段。
func TestForgingSelectRound16ForgeCarrierIsTheRawRowAndTheAliases(t *testing.T) {
	cases := []struct {
		name     string
		extraKey string
		rawTail  byte
		want     byte
	}{
		{name: "byte_57 alias", extraKey: "byte_57", want: forgingSelectRound16ForgeLevel},
		{name: "value_57 alias", extraKey: "value_57", want: forgingSelectRound16ForgeLevel},
		{name: "raw row tail alone already carries it", rawTail: 7, want: 7},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(loginEquipmentTailReassertEnv, "")
			repositories := dnfrepomemory.NewMemoryGroup()
			seedForgingSelectRound16Character(t, repositories)
			record, found, err := repositories.Equipment.Load(context.Background(), strconv.Itoa(forgingSelectRound16CharacterID))
			if err != nil || !found {
				t.Fatalf("load seeded equipment: found=%t err=%v", found, err)
			}
			entry := record.Entries[strconv.Itoa(forgingSelectRound16WeaponSlot)]
			delete(entry.Extra, "byte_57")
			delete(entry.Extra, "value_57")
			if testCase.extraKey != "" {
				entry.Extra[testCase.extraKey] = strconv.Itoa(forgingSelectRound16ForgeLevel)
			}
			entry.RawEntry[loginEquipmentTailBlockFirstByte] = testCase.rawTail
			record.Entries[strconv.Itoa(forgingSelectRound16WeaponSlot)] = entry
			if err := repositories.Equipment.Save(context.Background(), record); err != nil {
				t.Fatalf("save carrier fixture: %v", err)
			}

			connection := &bufferConn{}
			service := newLoginEquipmentTestService(repositories)
			session := newLoginEquipmentTestSession(connection, forgingSelectRound16CharacterID)
			if err := service.sendLoginEquipmentWornSlotRow(session, forgingSelectRound16WeaponSlot, "round16_carrier"); err != nil {
				t.Fatalf("worn slot carrier: %v", err)
			}
			row := forgingSelectRound16SingleOp14Row(t, connection.write.Bytes())
			if got := row[loginEquipmentTailBlockFirstByte]; got != testCase.want {
				t.Fatalf("delivered forge(+0x57) = %d, want %d (row=%x)", got, testCase.want, row)
			}
		})
	}
}
