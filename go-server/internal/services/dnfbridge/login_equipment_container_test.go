package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfitemlock "longheng.io/server/internal/modules/dnf/itemlock"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// liveCharacter6EquipmentRow 是 2026-09-28 13:24:19Z 现场
// `game-upper-selected-userinfo-mode1-equipment-create-rows`（角色 6，entry_count=30）
// 那份 row_summary 逐条抄下来的槽位/物品对 —— 不是编的夹具：
// `0:42601 1:42603 2:42605 3:42609 4:42611 5:42615 6:42607 7:42613 8:101580204
//
//	9:101590068 11:101590023 12:101040493(武器) 13:400330106 14:100070551 15:100170519
//	16:100120544 17:100270515 18:100220519 19:400300070 20:400310070 21:400320074
//	22:100344527 23:100352839 25:100390011 26:400990170 27:10006783 28:10006784
//	29:10006785 30:2747069 32:100380058`
//
// 槽 10 / 24 本来就没有穿戴（live 日志里也没有这两行），所以这里是 30 行。
type liveCharacter6EquipmentRow struct {
	slot   int16
	itemID int64
}

func liveCharacter6EquipmentRows() []liveCharacter6EquipmentRow {
	return []liveCharacter6EquipmentRow{
		{0, 42601}, {1, 42603}, {2, 42605}, {3, 42609}, {4, 42611},
		{5, 42615}, {6, 42607}, {7, 42613}, {8, 101580204}, {9, 101590068},
		{11, 101590023}, {12, 101040493}, {13, 400330106}, {14, 100070551}, {15, 100170519},
		{16, 100120544}, {17, 100270515}, {18, 100220519}, {19, 400300070}, {20, 400310070},
		{21, 400320074}, {22, 100344527}, {23, 100352839}, {25, 100390011}, {26, 400990170},
		{27, 10006783}, {28, 10006784}, {29, 10006785}, {30, 2747069}, {32, 100380058},
	}
}

// seedLiveCharacter6 建出与线上同形的角色 6：角色记录 + 背包记录（**没有** 0:16 那件
// 幻影物品，与现场 `dnf_inventory_items` 一致）+ 上面那 30 条已穿戴行。
func seedLiveCharacter6(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "6",
		AccountID:   defaultAccountPrefix + "10011",
		Name:        "test78",
		Level:       90,
		Stats:       map[string]int64{"gold": 1000000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots:       map[string]dnfrepo.ItemStack{"0:11": {ItemID: 101020668, Count: 1}},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]dnfrepo.EquipmentEntry, len(liveCharacter6EquipmentRows()))
	for _, row := range liveCharacter6EquipmentRows() {
		entries[strconv.Itoa(int(row.slot))] = dnfrepo.EquipmentEntry{
			SlotIndex: row.slot,
			ItemID:    row.itemID,
			RawEntry:  make([]byte, currentItemListEntryWireSize),
			Extra: map[string]string{
				// 线上每一行的 currentMode1EquipmentType 都等于槽号
				// （日志 `12:101040493:type=12`），行投影因此稳定。
				"current_exe_equipment_type": strconv.Itoa(int(row.slot)),
			},
		}
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "6",
		Entries:     entries,
	}); err != nil {
		t.Fatal(err)
	}
}

func newLoginEquipmentTestService(repositories dnfrepo.Group) *Service {
	return &Service{
		options: options{
			accountPrefix:      defaultAccountPrefix,
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
}

func newLoginEquipmentTestSession(connection *bufferConn, characterID uint16) *gameSession {
	return &gameSession{
		conn:                connection,
		connID:              "login-equipment",
		accountID:           defaultAccountPrefix + "10011",
		channel:             channelcatalog.Channel{ID: 252},
		selectedCharacterID: characterID,
	}
}

// splitLoginBootstrapContainers 按**生效档**的容器清单切出引导段，并返回每个
// list 的容器体（key = list type）。它用的清单就是被测代码用的那一份
// （`loginEquipmentBootstrapListTypes()`），所以档位切换后用例自动跟着走。
func splitLoginBootstrapContainers(t *testing.T, stream []byte) (map[byte][]byte, []byte) {
	t.Helper()
	listTypes := loginEquipmentBootstrapListTypes()
	bodies := make(map[byte][]byte, len(listTypes))
	for index, wantType := range listTypes {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) {
			t.Fatalf("bootstrap packet %d (list %d) = class %d msg %d", index, wantType, packet.Header.Classification, packet.Header.MsgID)
		}
		if len(packet.Body) < 3 || packet.Body[0] != wantType {
			t.Fatalf("bootstrap packet %d body=%x, want list %d", index, packet.Body, wantType)
		}
		bodies[wantType] = packet.Body
	}
	return bodies, stream
}

func loginItemListRowIDs(body []byte) map[int16]uint32 {
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	out := make(map[int16]uint32, count)
	for index := 0; index < count; index++ {
		row := body[3+index*currentItemListEntryWireSize:]
		out[int16(binary.LittleEndian.Uint16(row[0:2]))] = binary.LittleEndian.Uint32(row[2:6])
	}
	return out
}

// TestLoginEquipmentContainerIsSentAtCharacterSelectWithLiveCharacterShape 是这次
// 修复的主用例：登录/选角引导必须把 list 3（已穿戴）作为**第七个容器**建立出来，
// 行数就是线上那 30 条，槽 12 的武器（101040493）与槽 17（100270515）都必须在里面。
func TestLoginEquipmentContainerIsSentAtCharacterSelectWithLiveCharacterShape(t *testing.T) {
	t.Setenv(loginEquipmentContainerEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter6(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	connection := &bufferConn{}
	session := newLoginEquipmentTestSession(connection, 6)

	if err := service.sendCurrentSelectInventoryBootstrap(session, "login_equipment_test"); err != nil {
		t.Fatal(err)
	}
	bodies, rest := splitLoginBootstrapContainers(t, connection.write.Bytes())
	want := []byte{0, 1, 2, 7, 12, currentGuildMedalInventoryListType, currentSocketListEquipment}
	if len(loginEquipmentBootstrapListTypes()) != len(want) {
		t.Fatalf("bootstrap list count = %d, want %d", len(loginEquipmentBootstrapListTypes()), len(want))
	}
	for index, listType := range loginEquipmentBootstrapListTypes() {
		if listType != want[index] {
			t.Fatalf("bootstrap list %d = %d, want %d", index, listType, want[index])
		}
	}
	equipmentBody, ok := bodies[currentSocketListEquipment]
	if !ok {
		t.Fatalf("login bootstrap never sent list 3; containers=%v", bodies)
	}
	if got := int(binary.LittleEndian.Uint16(equipmentBody[1:3])); got != len(liveCharacter6EquipmentRows()) {
		t.Fatalf("list 3 row count = %d, want %d", got, len(liveCharacter6EquipmentRows()))
	}
	if wantLen := 3 + len(liveCharacter6EquipmentRows())*currentItemListEntryWireSize; len(equipmentBody) != wantLen {
		t.Fatalf("list 3 body_len = %d, want %d", len(equipmentBody), wantLen)
	}
	rows := loginItemListRowIDs(equipmentBody)
	if rows[12] != 101040493 {
		t.Fatalf("list 3 slot 12 item = %d, want 101040493 (live weapon)", rows[12])
	}
	if rows[17] != 100270515 {
		t.Fatalf("list 3 slot 17 item = %d, want 100270515", rows[17])
	}
	if _, present := rows[10]; present {
		t.Fatalf("list 3 carries slot 10 although the live character has no row there: %v", rows)
	}
	// 引导段之后必须紧跟 item-lock 快照，且没有多余字节（顺序与包数都被钉住）。
	trailing, remaining := splitCurrentGameServerUpperPacketAuto(t, rest)
	if trailing.Header.MsgID != dnfitemlock.LockListMessageID {
		t.Fatalf("packet after containers = msg %d, want item-lock snapshot", trailing.Header.MsgID)
	}
	if len(remaining) != 0 {
		t.Fatalf("bootstrap trailing bytes=%x", remaining)
	}
}

// TestLoginEquipmentContainerRollbackIsByteForByteSixContainers 是回退字面量的用例：
// 写 suppressed 之后，引导段必须**恰好**等于修复档去掉 list 3 那一个包之后的字节
// —— 顺序、每包包体、其余六个容器逐字节相同。
func TestLoginEquipmentContainerRollbackIsByteForByteSixContainers(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter6(t, repositories)
	service := newLoginEquipmentTestService(repositories)

	t.Setenv(loginEquipmentContainerEnv, "")
	fixConnection := &bufferConn{}
	if err := service.sendCurrentSelectInventoryBootstrap(
		newLoginEquipmentTestSession(fixConnection, 6),
		"login_equipment_fix",
	); err != nil {
		t.Fatal(err)
	}
	fixStream := append([]byte(nil), fixConnection.write.Bytes()...)

	t.Setenv(loginEquipmentContainerEnv, loginEquipmentContainerSuppressed)
	rollbackConnection := &bufferConn{}
	if err := service.sendCurrentSelectInventoryBootstrap(
		newLoginEquipmentTestSession(rollbackConnection, 6),
		"login_equipment_rollback",
	); err != nil {
		t.Fatal(err)
	}
	rollbackStream := rollbackConnection.write.Bytes()

	for _, listType := range currentSelectInventoryBootstrapListTypesRollback {
		if listType == currentSocketListEquipment {
			t.Fatal("rollback list set must not contain list 3")
		}
	}
	bodies, _ := splitLoginBootstrapContainers(t, rollbackStream)
	if _, present := bodies[currentSocketListEquipment]; present {
		t.Fatalf("rollback bootstrap still sent list 3: %v", bodies)
	}
	if len(loginEquipmentBootstrapListTypes()) != len(currentSelectInventoryBootstrapListTypesRollback) {
		t.Fatalf("rollback list count = %d, want %d", len(loginEquipmentBootstrapListTypes()), len(currentSelectInventoryBootstrapListTypesRollback))
	}
	// 定位修复档里 list 3 那个包（它是最后一个容器包），整段删掉后必须与回退档逐字节相同。
	offset := 0
	equipmentOffset := -1
	equipmentLength := 0
	for index, listType := range currentSelectInventoryBootstrapListTypes {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, fixStream[offset:])
		packetLength := len(fixStream[offset:]) - len(rest)
		if listType == currentSocketListEquipment {
			if index != len(currentSelectInventoryBootstrapListTypes)-1 {
				t.Fatalf("list 3 is container %d, want the last container", index)
			}
			equipmentOffset = offset
			equipmentLength = packetLength
		}
		if len(packet.Body) == 0 {
			t.Fatalf("empty container body at %d", offset)
		}
		offset += packetLength
	}
	if equipmentOffset < 0 {
		t.Fatal("list 3 container missing from the fix-mode stream")
	}
	expectedRollback := append([]byte(nil), fixStream[:equipmentOffset]...)
	expectedRollback = append(expectedRollback, fixStream[equipmentOffset+equipmentLength:]...)
	if !bytes.Equal(expectedRollback, rollbackStream) {
		t.Fatalf("rollback stream differs from fix-minus-list3: fix=%d rollback=%d expected=%d",
			len(fixStream), len(rollbackStream), len(expectedRollback))
	}
}

// TestLoginEquipmentContainerEmitsEmptyContainerWhenEquipmentRecordAbsent 钉住
// "记录缺失 = 真实空装备栏"：没有 equipment 记录的角色也必须拿到一个空 list 3 容器
// （`03 0000`），而不是让整个引导失败或者少一个包。
func TestLoginEquipmentContainerEmitsEmptyContainerWhenEquipmentRecordAbsent(t *testing.T) {
	t.Setenv(loginEquipmentContainerEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "91",
		AccountID:   defaultAccountPrefix + "10011",
		Stats:       map[string]int64{"gold": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "91",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := newLoginEquipmentTestService(repositories)
	connection := &bufferConn{}
	if err := service.sendCurrentSelectInventoryBootstrap(
		newLoginEquipmentTestSession(connection, 91),
		"login_equipment_absent",
	); err != nil {
		t.Fatal(err)
	}
	bodies, _ := splitLoginBootstrapContainers(t, connection.write.Bytes())
	equipmentBody, ok := bodies[currentSocketListEquipment]
	if !ok {
		t.Fatalf("absent equipment record dropped the list 3 container: %v", bodies)
	}
	if !bytes.Equal(equipmentBody, []byte{currentSocketListEquipment, 0, 0}) {
		t.Fatalf("absent equipment container body = %x, want 030000", equipmentBody)
	}
}

// loginPhantomOp19Body 是现场 13:24:21.465Z 那条 op19 的**包体原字节**
// （RECV 行 41 字节里的后 28 字节）：
//
//	001000 00000000 01000000 03 0c00 6dc10506 00000000 ffffffff 0000
//	src=(list0 主背包,16,IV=0) count=1 dst=(list3 装备,12,IV=0x0605C16D)
//
// 服务端自己的解码在 packet_log 里写作
// `src=(0,16,0x00000000) count=1 dst=(3,12,0x0605C16D) dstStack=0 actor=-1 tail=(0,0)`。
const loginPhantomOp19Hex = "0010000000000001000000030c006dc1050600000000ffffffff0000"

func loginPhantomOp19Body(t *testing.T) []byte {
	t.Helper()
	body, err := hex.DecodeString(loginPhantomOp19Hex)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 28 {
		t.Fatalf("phantom op19 body len=%d, want 28", len(body))
	}
	return body
}

func loginMoveAckBody(t *testing.T, moveValue uint32) []byte {
	t.Helper()
	body := make([]byte, 11)
	body[0] = 1
	body[1] = 0
	binary.LittleEndian.PutUint16(body[2:4], 16)
	binary.LittleEndian.PutUint32(body[4:8], moveValue)
	body[8] = currentSocketListEquipment
	binary.LittleEndian.PutUint16(body[9:11], 12)
	return body
}

func loginMoveResult(t *testing.T, moveValue uint32) alignedcmd.Result {
	t.Helper()
	return alignedcmd.Result{
		Operation: "move_itemspace",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketMoveItemspace),
			Body:           loginMoveAckBody(t, moveValue),
			Classification: dnfproto.DefaultChannelClassification,
		}},
	}
}

// TestLoginEquipmentNoopConvergenceOnlyFiresOnTheLoginWearNoopBranch 钉住"什么时候
// 才允许补两端点权威行"。判据缺一不可，任何一条不成立都必须是**零行为**
// —— 这就是"穿脱路径逐字节不变"在代码上的证明。
//
// 2026-09-28 第五轮：窗口关闭**不再**等于"不规划"。方向判定本身
// （equip/worn_slot_reassert_gate.go 的 worn_slot_reassert / worn_slot_stale_claim）
// 与窗口无关，它产生的 noop 同样需要端点权威行去纠正客户端的幻影副本，
// 因此"窗口关 + 本族复述形状 + ACK 的 moveValue==0"现在**要规划**（用例 1）；
// 形状不符（用例 3）或不是 noop（用例 2）时仍然零行为。
func TestLoginEquipmentNoopConvergenceOnlyFiresOnTheLoginWearNoopBranch(t *testing.T) {
	t.Setenv(loginEquipmentSlotRefreshEnv, "")
	service := &Service{}
	session := &gameSession{connID: "login-equipment-plan", selectedCharacterID: 6, channel: channelcatalog.Channel{ID: 252}}
	body := loginPhantomOp19Body(t)

	// 1) 窗口关闭 + 本族复述形状 + ACK 的 moveValue==0：**要规划**（第五轮：
	//    这条 noop 由方向判定产生，与窗口无关，客户端那一格/那一身的幻影副本靠它纠正）。
	closed := service.planLoginEquipmentNoopConvergence(session, body, false, loginMoveResult(t, 0))
	if !closed.planned || !closed.hasSource || !closed.hasWorn {
		t.Fatalf("closed-window claim noop not planned: %+v", closed)
	}
	if closed.sourceList != loginWearFamilySourceListMain || closed.sourceSlot != 16 || closed.wornSlot != 12 {
		t.Fatalf("closed-window plan endpoints = (%d,%d)/(%d), want (0,16)/(12)", closed.sourceList, closed.sourceSlot, closed.wornSlot)
	}
	// 2) 窗口关闭 + 本族形状、但这**不是** noop（真落库移动，moveValue=1）：零行为。
	if got := service.planLoginEquipmentNoopConvergence(session, body, false, loginMoveResult(t, 1)); got.planned {
		t.Fatalf("closed-window changed move still planned: %+v", got)
	}
	// 3) 窗口关闭 + **非本族形状**的 noop：零行为（identity_echo / 双端点同名 / 普通搬运
	//    都不在方向判定的形状内，方向判定也不可能给它们具名）。
	for name, other := range map[string][]byte{
		"identity_echo":   loginWearBridgeEchoBody(),
		"ordinary_bag":    loginWearBridgeOrdinaryBody(),
		"double_identity": loginWearBridgeBody(0, 11, 101040493, currentSocketListEquipment, 12, 101040493),
	} {
		if got := service.planLoginEquipmentNoopConvergence(session, other, false, loginMoveResult(t, 0)); got.planned {
			t.Fatalf("%s: closed-window non-claim noop still planned: %+v", name, got)
		}
	}
	// 4) 窗口开、但这**不是** noop（真落库移动，moveValue=1）：规划为空。
	if got := service.planLoginEquipmentNoopConvergence(session, body, true, loginMoveResult(t, 1)); got.planned {
		t.Fatalf("changed move still planned: %+v", got)
	}
	// 5) 窗口开 + noop：两端点都要点名，源端点必须是客户端自己写的 (0,16)。
	got := service.planLoginEquipmentNoopConvergence(session, body, true, loginMoveResult(t, 0))
	if !got.planned || !got.hasSource || !got.hasWorn {
		t.Fatalf("login noop not planned: %+v", got)
	}
	if got.sourceList != loginWearFamilySourceListMain || got.sourceSlot != 16 {
		t.Fatalf("source endpoint = (%d,%d), want (0,16)", got.sourceList, got.sourceSlot)
	}
	if got.wornSlot != 12 {
		t.Fatalf("worn slot = %d, want 12", got.wornSlot)
	}
	if got.switchValue != loginEquipmentSlotRefreshOn {
		t.Fatalf("switch value = %q, want %q", got.switchValue, loginEquipmentSlotRefreshOn)
	}
	// 6) 回退字面量：整条路径归零（窗口开与窗口关两种都归零）。
	t.Setenv(loginEquipmentSlotRefreshEnv, loginEquipmentSlotRefreshOff)
	if got := service.planLoginEquipmentNoopConvergence(session, body, true, loginMoveResult(t, 0)); got.planned {
		t.Fatalf("rollback literal still planned: %+v", got)
	}
	if got := service.planLoginEquipmentNoopConvergence(session, body, false, loginMoveResult(t, 0)); got.planned {
		t.Fatalf("rollback literal still planned for the closed-window claim noop: %+v", got)
	}
}

// TestLoginEquipmentNoopSlotRowsCarryBothLiveEndpoints 是这个收敛修复的主用例：
// noop 之后必须补两条逐槽行 —— list 0 槽 16（仓储里没有 = 空行，正是客户端缺的
// "那个槽是空的"）与 list 3 槽 12（仓储里的武器 101040493）。
func TestLoginEquipmentNoopSlotRowsCarryBothLiveEndpoints(t *testing.T) {
	t.Setenv(loginEquipmentSlotRefreshEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter6(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	connection := &bufferConn{}
	session := newLoginEquipmentTestSession(connection, 6)

	service.sendLoginEquipmentNoopSlotRows(session, loginEquipmentNoopConvergence{
		planned:     true,
		sourceList:  loginWearFamilySourceListMain,
		sourceSlot:  16,
		hasSource:   true,
		wornSlot:    12,
		hasWorn:     true,
		switchValue: loginEquipmentSlotRefreshOn,
	}, "login_equipment_test")

	stream := connection.write.Bytes()
	// 逐槽行是普通 upper 包（class0/msg14 + 固定 16 字节头），**第二个包起**
	// header 首字节是 upper 序号（非 0），所以这里显式按 16 字节头切。
	sourcePacket, rest := splitGameServerUpperPacketWithHeader(t, stream, dnfproto.GameServerUpperHeaderSize16)
	if sourcePacket.Header.Classification != 0 || sourcePacket.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("source row packet = class %d msg %d", sourcePacket.Header.Classification, sourcePacket.Header.MsgID)
	}
	if len(sourcePacket.Body) != 3+currentItemListEntryWireSize ||
		sourcePacket.Body[0] != loginWearFamilySourceListMain ||
		binary.LittleEndian.Uint16(sourcePacket.Body[1:3]) != 1 {
		t.Fatalf("source row body=%x", sourcePacket.Body)
	}
	sourceRow := sourcePacket.Body[3:]
	if slot := binary.LittleEndian.Uint16(sourceRow[0:2]); slot != 16 {
		t.Fatalf("source row slot = %d, want 16", slot)
	}
	// 空行必须写**已验证的空行哨兵** 0xFFFFFFFF，不许写 itemID=0：
	// 运行时 PVF 的 item 0 是 `stackable/gold.stk`（金币），客户端会把它实例化到
	// 本地那一格上，玩家刚脱到该格的装备行因此被整体覆盖（+0x0B..0x0C 耐久字变 0）
	// —— 这就是"全身防具穿脱后耐久显示 0"那条故障。见 empty_slot_row_encoding.go。
	if itemID := binary.LittleEndian.Uint32(sourceRow[2:6]); itemID != emptySlotItemIDCurrentEXE {
		t.Fatalf("source row item = %#x, want the verified empty-slot sentinel %#x (the live bag has no 0:16)",
			itemID, emptySlotItemIDCurrentEXE)
	}

	wornPacket, trailing := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
	if wornPacket.Header.Classification != 0 || wornPacket.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("worn row packet = class %d msg %d", wornPacket.Header.Classification, wornPacket.Header.MsgID)
	}
	if len(wornPacket.Body) != 3+currentItemListEntryWireSize ||
		wornPacket.Body[0] != currentSocketListEquipment ||
		binary.LittleEndian.Uint16(wornPacket.Body[1:3]) != 1 {
		t.Fatalf("worn row body=%x", wornPacket.Body)
	}
	wornRow := wornPacket.Body[3:]
	if slot := binary.LittleEndian.Uint16(wornRow[0:2]); slot != 12 {
		t.Fatalf("worn row slot = %d, want 12", slot)
	}
	if itemID := binary.LittleEndian.Uint32(wornRow[2:6]); itemID != 101040493 {
		t.Fatalf("worn row item = %d, want 101040493 (live weapon)", itemID)
	}
	if len(trailing) != 0 {
		t.Fatalf("trailing bytes=%x", trailing)
	}
}

// TestLoginEquipmentNoopSlotRowsSkipAbsentWornSlot 钉住"不发没证据的包"：
// list 3 的空行形状在现场没有证据，所以仓储里没有那一格时只发源端点那一行。
func TestLoginEquipmentNoopSlotRowsSkipAbsentWornSlot(t *testing.T) {
	t.Setenv(loginEquipmentSlotRefreshEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter6(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	connection := &bufferConn{}
	session := newLoginEquipmentTestSession(connection, 6)

	service.sendLoginEquipmentNoopSlotRows(session, loginEquipmentNoopConvergence{
		planned:     true,
		sourceList:  loginWearFamilySourceListMain,
		sourceSlot:  16,
		hasSource:   true,
		wornSlot:    10,
		hasWorn:     true,
		switchValue: loginEquipmentSlotRefreshOn,
	}, "login_equipment_absent_worn")

	packet, rest := splitGameServerUpperPacketWithHeader(t, connection.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
	if packet.Body[0] != loginWearFamilySourceListMain {
		t.Fatalf("first packet list=%d, want list 0", packet.Body[0])
	}
	if len(rest) != 0 {
		t.Fatalf("absent worn slot still produced a list-3 row: %x", rest)
	}
}

// TestLoginEquipmentSlotRefreshRollbackSendsNothing 钉住 DNFBRIDGE_LOGIN_EQUIPMENT_SLOT_REFRESH=off
// 的一键回退：noop 之后一个字节都不补（今天的行为）。
func TestLoginEquipmentSlotRefreshRollbackSendsNothing(t *testing.T) {
	t.Setenv(loginEquipmentSlotRefreshEnv, loginEquipmentSlotRefreshOff)
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter6(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	connection := &bufferConn{}
	session := newLoginEquipmentTestSession(connection, 6)

	plan := service.planLoginEquipmentNoopConvergence(session, loginPhantomOp19Body(t), true, loginMoveResult(t, 0))
	if plan.planned {
		t.Fatalf("rollback literal planned a refresh: %+v", plan)
	}
	service.sendLoginEquipmentNoopSlotRows(session, plan, "login_equipment_rollback")
	if connection.write.Len() != 0 {
		t.Fatalf("rollback literal wrote %d bytes: %x", connection.write.Len(), connection.write.Bytes())
	}
}

// TestLoginEquipmentSlotRefreshLeavesRealEquipMoveUntouched 是"不碰穿脱路径"的正面钉子：
//
//   - 默认档下穿脱专用后置动作仍然是 suppressed（list_types=[]、零个 op13）；
//   - 真落库的移动（moveValue!=0）即使落在登录窗口内，本文件的规划也必须为空
//     ⇒ `sendLoginEquipmentNoopSlotRows` 一个字节都不写。
func TestLoginEquipmentSlotRefreshLeavesRealEquipMoveUntouched(t *testing.T) {
	t.Setenv(loginEquipmentSlotRefreshEnv, "")
	t.Setenv(equipMoveContainerRefreshEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter6(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	connection := &bufferConn{}
	session := newLoginEquipmentTestSession(connection, 6)

	if got := service.equipMoveContainerRefreshMode(); got != equipMoveContainerRefreshSuppressed {
		t.Fatalf("equip move container refresh mode = %q, want %q", got, equipMoveContainerRefreshSuppressed)
	}
	if err := service.sendAlignedPostAction(session, alignedcmd.Result{
		Operation: "move_itemspace",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketMoveItemspace),
			Body:           loginMoveAckBody(t, 1),
			Classification: dnfproto.DefaultChannelClassification,
		}},
	}, alignedcmd.PostActionRefreshSelectedWearContainers); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("suppressed wear-move post action wrote %d bytes: %x", connection.write.Len(), connection.write.Bytes())
	}

	// 真移动（moveValue=1）落在登录窗口内：规划为空、发送为空。
	plan := service.planLoginEquipmentNoopConvergence(session, loginPhantomOp19Body(t), true, loginMoveResult(t, 1))
	if plan.planned {
		t.Fatalf("real equip move planned a login refresh: %+v", plan)
	}
	service.sendLoginEquipmentNoopSlotRows(session, plan, "login_equipment_real_move")
	if connection.write.Len() != 0 {
		t.Fatalf("real equip move wrote %d bytes from the login path: %x", connection.write.Len(), connection.write.Bytes())
	}
}
