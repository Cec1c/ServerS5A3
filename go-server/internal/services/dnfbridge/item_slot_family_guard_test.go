package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是「道具家族 ↔ 背包分页」闸门（item_slot_family_guard.go，操作者清单第 48 条）的钉子：
//
//  1. 分页表逐段边界（每段首/末槽必须通过，越界一槽必须命中）；
//  2. 装备拖进材料页在 enforce 档被拦且**只回 4 字节厂商失败体**；同级合法移动不被拦；
//  3. log 档零回包、零行为变化（与 off 档包字节逐字节相同），只多一条事件；
//  4. off 档连事件都没有；
//  5. 家族取不到 / 目标不在任何已知分页 / mainExpandStage 无等价物 ⇒ 退化为不校验。

func itemSlotFamilyGuardTestStack(itemID int64, extra map[string]string) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{ItemID: itemID, Count: 1, Extra: extra}
}

func itemSlotFamilyGuardEquipmentStack(itemID int64, equipmentType string) dnfrepo.ItemStack {
	return itemSlotFamilyGuardTestStack(itemID, map[string]string{
		"item_kind":      "equipment",
		"equipment_type": equipmentType,
		"pvf_path":       "equipment/test/guard.equ",
	})
}

func itemSlotFamilyGuardStackableStack(itemID int64, stackableType string) dnfrepo.ItemStack {
	return itemSlotFamilyGuardTestStack(itemID, map[string]string{
		"item_kind":      "stackable",
		"stackable_type": stackableType,
		"pvf_path":       "stackable/test/guard.stk",
	})
}

// 逐段边界：每段首/末槽**不命中**（合法），越界一槽**命中**（明显不匹配）。
func TestCurrentItemSlotFamilyGuardPageBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		stack         dnfrepo.ItemStack
		okSlots       []int16 // 段首/段末：必须不命中
		mismatchSlots []int16 // 越界一槽：必须命中
	}{
		{
			name:    "装备段 Main 9..64",
			stack:   itemSlotFamilyGuardEquipmentStack(101, "[weapon]"),
			okSlots: []int16{9, 64},
			// 8 是快捷格（Main 3..8）⇒ 不属于任何已知页 ⇒ 本层不判（厂商同样把快捷格
			// 交给 CanPlaceInMainQuickSlot 单独判）。
			mismatchSlots: []int16{65, 120, 121},
		},
		{
			name:          "消耗品段 Main 65..120",
			stack:         itemSlotFamilyGuardStackableStack(102, "[waste]"),
			okSlots:       []int16{65, 120},
			mismatchSlots: []int16{64, 121, 9},
		},
		{
			name:          "材料段 Main 121..176",
			stack:         itemSlotFamilyGuardStackableStack(103, "[material]"),
			okSlots:       []int16{121, 176},
			mismatchSlots: []int16{120, 177, 65},
		},
		{
			name:          "任务品段 Main 177..232",
			stack:         itemSlotFamilyGuardStackableStack(104, "[quest]"),
			okSlots:       []int16{177, 232},
			mismatchSlots: []int16{176, 233, 121},
		},
		{
			name:          "副职业材料段 Main 233..288",
			stack:         itemSlotFamilyGuardStackableStack(105, "[material expert job]"),
			okSlots:       []int16{233, 288},
			mismatchSlots: []int16{232, 289, 176},
		},
		{
			name:          "徽章段 Main 289..344（我仓口径；厂商 289..351）",
			stack:         itemSlotFamilyGuardStackableStack(106, "[avatar emblem]"),
			okSlots:       []int16{289, 344},
			mismatchSlots: []int16{288, 233},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, slot := range test.okSlots {
				if decision, mismatch := currentItemSlotFamilyGuardMismatch(test.stack, dnfrepo.MainInventoryListType, slot); mismatch {
					t.Fatalf("slot %d must stay legal, got %+v", slot, decision)
				}
			}
			for _, slot := range test.mismatchSlots {
				decision, mismatch := currentItemSlotFamilyGuardMismatch(test.stack, dnfrepo.MainInventoryListType, slot)
				if !mismatch {
					t.Fatalf("slot %d must be a family/page mismatch", slot)
				}
				if decision.Reason != "family_page_mismatch" {
					t.Fatalf("slot %d reason = %q", slot, decision.Reason)
				}
			}
		})
	}
}

// 勋章容器（wire list 38）的两个 49 格分页：勋章 0..48、守护珠 49..97。
func TestCurrentItemSlotFamilyGuardGuildMedalPages(t *testing.T) {
	medal := itemSlotFamilyGuardEquipmentStack(201, "[flag]")
	gem := itemSlotFamilyGuardStackableStack(202, "[flag gem]")

	for _, slot := range []int16{0, 48} {
		if decision, mismatch := currentItemSlotFamilyGuardMismatch(medal, currentGuildMedalInventoryListType, slot); mismatch {
			t.Fatalf("medal page slot %d must stay legal, got %+v", slot, decision)
		}
	}
	for _, slot := range []int16{49, 97} {
		if decision, mismatch := currentItemSlotFamilyGuardMismatch(medal, currentGuildMedalInventoryListType, slot); !mismatch {
			t.Fatalf("medal into guardian-gem page slot %d must be a mismatch", slot)
		} else if decision.ExpectedFrom != currentGuildMedalPageSlotStart || decision.ExpectedTo != currentGuildMedalPageSlotEnd {
			t.Fatalf("medal expected range = %d..%d", decision.ExpectedFrom, decision.ExpectedTo)
		}
	}
	for _, slot := range []int16{49, 97} {
		if decision, mismatch := currentItemSlotFamilyGuardMismatch(gem, currentGuildMedalInventoryListType, slot); mismatch {
			t.Fatalf("guardian-gem page slot %d must stay legal, got %+v", slot, decision)
		}
	}
	for _, slot := range []int16{0, 48} {
		if _, mismatch := currentItemSlotFamilyGuardMismatch(gem, currentGuildMedalInventoryListType, slot); !mismatch {
			t.Fatalf("guardian gem into medal page slot %d must be a mismatch", slot)
		}
	}
	// 勋章容器外的槽（98 起）不属于任何已知页 ⇒ 不校验（越界由既有 guildMedalContainerSlotContains 拦）。
	for _, slot := range []int16{98, 130} {
		if _, mismatch := currentItemSlotFamilyGuardMismatch(medal, currentGuildMedalInventoryListType, slot); mismatch {
			t.Fatalf("out-of-page slot %d must not be judged by this layer", slot)
		}
	}
}

// 退化为"不校验该维度"的全部情形：家族取不到、期望容器不是目标容器、目标不在任何已知分页。
func TestCurrentItemSlotFamilyGuardDegradesInsteadOfRejecting(t *testing.T) {
	tests := []struct {
		name   string
		stack  dnfrepo.ItemStack
		list   byte
		slot   int16
		reason string
	}{
		{
			name:   "stackable 缺 stackable_type（现场库约 23% 的行）",
			stack:  itemSlotFamilyGuardTestStack(301, map[string]string{"item_kind": "stackable"}),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "family key missing",
		},
		{
			name:   "equipment 缺 equipment_type",
			stack:  itemSlotFamilyGuardTestStack(302, map[string]string{"item_kind": "equipment"}),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "family key missing",
		},
		{
			name:   "缺 item_kind（金币行/历史脏数据）",
			stack:  itemSlotFamilyGuardTestStack(303, map[string]string{"pvf_path": "stackable/material/x.stk"}),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "item_kind missing",
		},
		{
			name:   "item_kind 非 stackable/equipment（现场有 kind=etc）",
			stack:  dnfrepo.ItemStack{ItemID: 304, Count: 2, Extra: map[string]string{"item_kind": "etc"}},
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "item_kind not classified",
		},
		{
			name:   "item id 非正",
			stack:  itemSlotFamilyGuardStackableStack(0, "[material]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "item id missing",
		},
		{
			name:   "宠物消耗品（期望容器 list 7）不归本层判",
			stack:  itemSlotFamilyGuardStackableStack(305, "[feed]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "expected container differs",
		},
		{
			name:   "宠物本体（期望容器 list 7）不归本层判",
			stack:  itemSlotFamilyGuardEquipmentStack(306, "[creature]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "expected container differs",
		},
		{
			name:   "装扮（期望容器 list 1）不归本层判",
			stack:  itemSlotFamilyGuardEquipmentStack(307, "[coat avatar]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "expected container differs",
		},
		{
			name:   "公会勋章（期望容器 list 38）在背包里不归本层判",
			stack:  itemSlotFamilyGuardEquipmentStack(308, "[flag]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "expected container differs",
		},
		{
			name:   "我仓 [material*4] 的 345..359 扩展段不是对齐过的页 ⇒ 不猜",
			stack:  itemSlotFamilyGuardStackableStack(309, "[material4]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   130,
			reason: "expected page unknown",
		},
		{
			name:   "快捷格（Main 3..8）不属于任何已知页",
			stack:  itemSlotFamilyGuardEquipmentStack(310, "[weapon]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   5,
			reason: "destination page unknown",
		},
		{
			name:   "账号共享水晶/灵魂仓库（354..365）不属于任何已知页",
			stack:  itemSlotFamilyGuardStackableStack(311, "[material]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   355,
			reason: "destination page unknown",
		},
		{
			name:   "越界槽（≥360）不属于任何已知页",
			stack:  itemSlotFamilyGuardEquipmentStack(312, "[weapon]"),
			list:   dnfrepo.MainInventoryListType,
			slot:   400,
			reason: "destination page unknown",
		},
		{
			name:   "list 7 的分页不在本层页表（宠物分段由既有 petInventoryMoveSegment 拦）",
			stack:  itemSlotFamilyGuardStackableStack(313, "[feed]"),
			list:   currentPetInventoryListType,
			slot:   200,
			reason: "container not in the page table",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, mismatch := currentItemSlotFamilyGuardMismatch(test.stack, test.list, test.slot)
			if mismatch {
				t.Fatalf("%s must degrade to no-check, got %+v", test.reason, decision)
			}
		})
	}
}

// 候选筛选（零副作用）：只有"同容器 + 目标是已知分页"的 op19 才值得读一次背包。
func TestCurrentItemSlotFamilyGuardMoveCandidate(t *testing.T) {
	tests := []struct {
		name    string
		request dnfinventory.MoveItemspaceRequest
		want    bool
	}{
		{
			name:    "背包内跨页（装备 → 材料页）",
			request: dnfinventory.MoveItemspaceRequest{SourceListType: 0, DestinationListType: 0, DestinationSlotIndex: 130},
			want:    true,
		},
		{
			name:    "勋章容器内跨页",
			request: dnfinventory.MoveItemspaceRequest{SourceListType: 38, DestinationListType: 38, DestinationSlotIndex: 60},
			want:    true,
		},
		{
			name:    "跨容器（背包 → 宠物）",
			request: dnfinventory.MoveItemspaceRequest{SourceListType: 0, DestinationListType: 7, DestinationSlotIndex: 200},
			want:    false,
		},
		{
			name:    "目标在快捷格",
			request: dnfinventory.MoveItemspaceRequest{SourceListType: 0, DestinationListType: 0, DestinationSlotIndex: 5},
			want:    false,
		},
		{
			name:    "个人仓库（list 2）内移动",
			request: dnfinventory.MoveItemspaceRequest{SourceListType: 2, DestinationListType: 2, DestinationSlotIndex: 130},
			want:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := currentItemSlotFamilyGuardMoveCandidate(test.request); got != test.want {
				t.Fatalf("candidate = %t want %t", got, test.want)
			}
		})
	}
}

// 档位解析：默认 log；只有显式 off/0/false/legacy 才回退，非法值回退到 log 并标记 bad。
func TestItemSlotFamilyGuardSetting(t *testing.T) {
	tests := []struct {
		value   string
		want    string
		wantBad bool
	}{
		{value: "", want: itemSlotFamilyGuardLog},
		{value: "log", want: itemSlotFamilyGuardLog},
		{value: " LOG ", want: itemSlotFamilyGuardLog},
		{value: "enforce", want: itemSlotFamilyGuardEnforce},
		{value: "Enforce", want: itemSlotFamilyGuardEnforce},
		{value: "off", want: itemSlotFamilyGuardOff},
		{value: "0", want: itemSlotFamilyGuardOff},
		{value: "false", want: itemSlotFamilyGuardOff},
		{value: "legacy", want: itemSlotFamilyGuardOff},
		{value: "on", want: itemSlotFamilyGuardLog, wantBad: true},
		{value: "true", want: itemSlotFamilyGuardLog, wantBad: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv(itemSlotFamilyGuardEnv, test.value)
			mode, raw, bad := itemSlotFamilyGuardSetting()
			if mode != test.want || bad != test.wantBad {
				t.Fatalf("mode=%q bad=%t want mode=%q bad=%t (raw=%q)", mode, bad, test.want, test.wantBad, raw)
			}
		})
	}
}

// ---- 端到端（真实 op19 分发 → 闸门 → owner / 回包）----

const (
	itemSlotFamilyGuardTestCharacterID = "77"
	// 装备在装备页第 4 格；材料页第 10 格。
	itemSlotFamilyGuardTestEquipmentSlot int16 = 12
	itemSlotFamilyGuardTestSamePageSlot  int16 = 20
	itemSlotFamilyGuardTestMaterialSlot  int16 = 130
)

func itemSlotFamilyGuardTestSlots() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"0:12": {
			ItemID: 101040127,
			Count:  1,
			Extra: map[string]string{
				"item_kind":      "equipment",
				"equipment_type": "[weapon]",
				"pvf_path":       "equipment/character/swordman/weapon/beamsword/101040127.equ",
			},
		},
	}
}

func newItemSlotFamilyGuardFixture(t *testing.T) (*Service, *gameSession, *bufferConn, *packetLogger, string, dnfrepo.Group) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: itemSlotFamilyGuardTestCharacterID,
		Slots:       itemSlotFamilyGuardTestSlots(),
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:          logger,
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "item-slot-family-guard-test",
		accountID:           "dnf:1",
		selectedCharacterID: 77,
	}
	return service, session, conn, logger, logPath, repositories
}

// driveItemSlotFamilyGuardMove 走**真实入口**（handleAlignedGameCommand → op19 分发），
// 不是只测 builder。
func driveItemSlotFamilyGuardMove(t *testing.T, service *Service, session *gameSession, body []byte) {
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
		t.Fatal("op19 was not handled")
	}
}

// itemSlotFamilyGuardMoveBody 组装当前 28 字节 op19 体（与既有测试同形状）。
func itemSlotFamilyGuardMoveBody(sourceList byte, sourceSlot int16, destinationList byte, destinationSlot int16) []byte {
	body := make([]byte, 28)
	body[0] = sourceList
	binary.LittleEndian.PutUint16(body[1:3], uint16(sourceSlot))
	binary.LittleEndian.PutUint32(body[7:11], 1)
	body[11] = destinationList
	binary.LittleEndian.PutUint16(body[12:14], uint16(destinationSlot))
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
	return body
}

func assertItemSlotFamilyGuardInventoryUntouched(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	record, found, err := repositories.Inventory.Load(ctx, itemSlotFamilyGuardTestCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if stack, ok := record.Slots["0:12"]; !ok || stack.ItemID != 101040127 || stack.Count != 1 {
		t.Fatalf("source row changed: %+v (slots=%+v)", stack, record.Slots)
	}
	if stack, ok := record.Slots["0:130"]; ok {
		t.Fatalf("material page must stay empty, got %+v", stack)
	}
}

// enforce 档：装备（9..64）拖进材料页（121..176）必须被拦，且**只回厂商的 4 字节失败体**，
// 不写库、不发成功 ACK、不发任何刷新。
func TestItemSlotFamilyGuardEnforceRejectsEquipmentIntoMaterialPage(t *testing.T) {
	t.Setenv(itemSlotFamilyGuardEnv, itemSlotFamilyGuardEnforce)
	ctx := context.Background()
	service, session, conn, logger, logPath, repositories := newItemSlotFamilyGuardFixture(t)

	driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(0, itemSlotFamilyGuardTestEquipmentSlot, 0, itemSlotFamilyGuardTestMaterialSlot))

	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) {
		t.Fatalf("rejection packet header = %+v", packet.Header)
	}
	if want := []byte{0x00, currentItemSlotFamilyMoveErrorInvalidOperation, 0x00, 0x00}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("rejection body = % X want % X", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("rejection emitted trailing packets: % X", rest)
	}
	assertItemSlotFamilyGuardInventoryUntouched(t, ctx, repositories)

	logText := readPeerEndpointTestLog(t, logger, logPath)
	event := itemSlotFamilyGuardEventLine(t, logText)
	for _, field := range []string{
		"mode=enforce",
		"item_kind=equipment",
		"item_family=[weapon]",
		"item_id=101040127",
		"src_list=0",
		"src_slot=12",
		"dst_list=0",
		"dst_slot=130",
		"expected_list=0",
		"expected_slots=9-64",
		"dst_page_slots=121-176",
		"reason=family_page_mismatch",
	} {
		if !strings.Contains(event, field) {
			t.Fatalf("event is missing %q\nline=%s", field, event)
		}
	}
}

// enforce 档：同页合法移动（装备 12 → 20）必须照旧成功，ACK 逐字节与今天相同。
func TestItemSlotFamilyGuardEnforceKeepsSamePageMove(t *testing.T) {
	t.Setenv(itemSlotFamilyGuardEnv, itemSlotFamilyGuardEnforce)
	ctx := context.Background()
	service, session, conn, logger, logPath, repositories := newItemSlotFamilyGuardFixture(t)

	driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(0, itemSlotFamilyGuardTestEquipmentSlot, 0, itemSlotFamilyGuardTestSamePageSlot))

	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) ||
		packet.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("ack header = %+v", packet.Header)
	}
	if want := []byte{0x01, 0x00, 0x0C, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x14, 0x00}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("ack body = % X want % X", packet.Body, want)
	}
	if len(rest) != 0 {
		// 普通"源格 → 空格"移动在改动前就只回这一条 11 字节 ACK（不补容器刷新），
		// 闸门不得多出任何包。
		t.Fatalf("same-page move emitted unexpected trailing packets: % X", rest)
	}
	record, found, err := repositories.Inventory.Load(ctx, itemSlotFamilyGuardTestCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if stack, ok := record.Slots["0:20"]; !ok || stack.ItemID != 101040127 {
		t.Fatalf("destination row = %+v (slots=%+v)", stack, record.Slots)
	}
	if _, still := record.Slots["0:12"]; still {
		t.Fatalf("source row survived: %+v", record.Slots)
	}
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, itemSlotFamilyGuardMismatchEvent) {
		t.Fatalf("legal same-page move must not produce a guard event\n%s", logText)
	}
}

// 勋章容器内跨页（守护珠 → 勋章页）同样命中：闸门不是只为 list 0 写的。
func TestItemSlotFamilyGuardEnforceRejectsGuardianGemIntoMedalPage(t *testing.T) {
	t.Setenv(itemSlotFamilyGuardEnv, itemSlotFamilyGuardEnforce)
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: itemSlotFamilyGuardTestCharacterID,
		Slots: map[string]dnfrepo.ItemStack{
			"38:60": {
				ItemID: 89999,
				Count:  1,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"stackable_type": "[flag gem]",
					"pvf_path":       "stackable/guardian_gem/gem.stk",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:          logger,
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "item-slot-family-guard-gem", accountID: "dnf:1", selectedCharacterID: 77}

	driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(38, 60, 38, 5))

	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if want := []byte{0x00, currentItemSlotFamilyMoveErrorInvalidOperation, 38, 38}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("rejection body = % X want % X", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("rejection emitted trailing packets: % X", rest)
	}
	record, found, err := repositories.Inventory.Load(ctx, itemSlotFamilyGuardTestCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if stack, ok := record.Slots["38:60"]; !ok || stack.ItemID != 89999 {
		t.Fatalf("guardian gem source row changed: %+v (slots=%+v)", stack, record.Slots)
	}
	if _, moved := record.Slots["38:5"]; moved {
		t.Fatalf("guardian gem reached the medal page: %+v", record.Slots)
	}
	event := itemSlotFamilyGuardEventLine(t, readPeerEndpointTestLog(t, logger, logPath))
	for _, field := range []string{
		"item_kind=stackable",
		"item_family=[flag gem]",
		"src_list=38",
		"src_slot=60",
		"dst_list=38",
		"dst_slot=5",
		"expected_slots=49-97",
		"dst_page_slots=0-48",
		"reason=family_page_mismatch",
	} {
		if !strings.Contains(event, field) {
			t.Fatalf("event is missing %q\nline=%s", field, event)
		}
	}
}

// log 档（**默认档**）：零回包差异、零写库差异，只多一条事件；off 档连事件都没有。
func TestItemSlotFamilyGuardLogModeChangesNothingButOneEvent(t *testing.T) {
	ctx := context.Background()

	type run struct {
		packets  []byte
		logText  string
		slots    map[string]dnfrepo.ItemStack
		eventCnt int
	}
	execute := func(mode string) run {
		t.Setenv(itemSlotFamilyGuardEnv, mode)
		service, session, conn, logger, logPath, repositories := newItemSlotFamilyGuardFixture(t)
		driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(0, itemSlotFamilyGuardTestEquipmentSlot, 0, itemSlotFamilyGuardTestMaterialSlot))
		record, found, err := repositories.Inventory.Load(ctx, itemSlotFamilyGuardTestCharacterID)
		if err != nil || !found {
			t.Fatalf("load inventory found=%t err=%v", found, err)
		}
		text := readPeerEndpointTestLog(t, logger, logPath)
		return run{
			packets:  append([]byte(nil), conn.write.Bytes()...),
			logText:  text,
			slots:    record.Slots,
			eventCnt: strings.Count(text, itemSlotFamilyGuardMismatchEvent),
		}
	}

	off := execute(itemSlotFamilyGuardOff)
	logged := execute(itemSlotFamilyGuardLog)

	if off.eventCnt != 0 {
		t.Fatalf("off mode must not log any guard event (%d)", off.eventCnt)
	}
	if logged.eventCnt != 1 {
		t.Fatalf("log mode must log exactly one guard event, got %d", logged.eventCnt)
	}
	if !bytes.Equal(off.packets, logged.packets) {
		t.Fatalf("log mode changed the downstream bytes\n off=% X\n log=% X", off.packets, logged.packets)
	}
	if len(logged.packets) == 0 {
		t.Fatal("the illegal move must still be applied in log/off mode (no behaviour change)")
	}
	packet, _ := splitGameServerUpperPacket(t, logged.packets)
	if want := []byte{0x01, 0x00, 0x0C, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x82, 0x00}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("log mode ACK body = % X want % X", packet.Body, want)
	}
	if logged.slots["0:130"].ItemID != 101040127 {
		t.Fatalf("log mode must still write the requested destination: %+v", logged.slots)
	}
	if _, still := logged.slots["0:12"]; still {
		t.Fatalf("log mode must still clear the source: %+v", logged.slots)
	}
	// 除那一条事件外，日志逐行相同（表头带时间戳，故按行比较并剔除事件行与表头）。
	if got, want := stripItemSlotFamilyGuardLogLines(off.logText), stripItemSlotFamilyGuardLogLines(logged.logText); got != want {
		t.Fatalf("log mode changed the existing log stream\n off:\n%s\n log:\n%s", got, want)
	}

	event := itemSlotFamilyGuardEventLine(t, logged.logText)
	for _, field := range []string{
		"mode=log",
		"src_list=0",
		"src_slot=12",
		"dst_list=0",
		"dst_slot=130",
		"reason=family_page_mismatch",
	} {
		if !strings.Contains(event, field) {
			t.Fatalf("event is missing %q\nline=%s", field, event)
		}
	}
}

func stripItemSlotFamilyGuardLogLines(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, itemSlotFamilyGuardMismatchEvent) ||
			strings.HasPrefix(line, "=== LongHeng Go dnfbridge packet log started") {
			continue
		}
		// 行尾的 ` t=<RFC3339Nano>` 是 DNFBRIDGE_PACKET_LOG_TIMESTAMPS 的墙钟字段
		// （默认 on，见 packet_log.go 的 packetLogTimestampsEnv）：两次运行必然不同，
		// 且与"log 档是否改变了日志流"无关 ⇒ 比较前必须剔除，否则这条逐行断言会
		// 退化成"两次采样的时间戳不同"。剔除的是**最后一个** t= 字段（时间戳恒在行尾）。
		if index := strings.LastIndex(line, " t="); index >= 0 {
			line = line[:index]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func itemSlotFamilyGuardEventLine(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "kind="+itemSlotFamilyGuardMismatchEvent) {
			return line
		}
	}
	t.Fatalf("guard event missing\n%s", text)
	return ""
}

// 源格没有该道具（客户端空发/反向拆分）时不校验：交给 owner 既有路径，闸门不产生事件、
// 也不多回任何包（既有失败路径逐字节不变）。
func TestItemSlotFamilyGuardSkipsWhenSourceStackMissing(t *testing.T) {
	t.Setenv(itemSlotFamilyGuardEnv, itemSlotFamilyGuardEnforce)
	service, session, conn, logger, logPath, _ := newItemSlotFamilyGuardFixture(t)

	// 源格 40（装备页）是空的；目标是材料页。
	driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(0, 40, 0, 130))

	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, itemSlotFamilyGuardMismatchEvent) {
		t.Fatalf("empty source must not produce a guard event\n%s", logText)
	}
	if sent := conn.write.Bytes(); len(sent) != 0 {
		t.Fatalf("guard must not take over the owner's existing failure path, sent % X", sent)
	}
}

// 家族取不到时 enforce 档也放行（取不到就不猜）：现场约 23% 的行属于这一类。
func TestItemSlotFamilyGuardEnforceLetsUnclassifiedFamilyThrough(t *testing.T) {
	t.Setenv(itemSlotFamilyGuardEnv, itemSlotFamilyGuardEnforce)
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: itemSlotFamilyGuardTestCharacterID,
		Slots: map[string]dnfrepo.ItemStack{
			"0:12": {ItemID: 3277, Count: 1, Extra: map[string]string{"item_kind": "stackable"}},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:          logger,
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "item-slot-family-guard-unclassified", accountID: "dnf:1", selectedCharacterID: 77}

	driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(0, 12, 0, 130))

	packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if want := []byte{0x01, 0x00, 0x0C, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x82, 0x00}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("unclassified family must move as before, ack = % X want % X", packet.Body, want)
	}
	if record, found, err := repositories.Inventory.Load(ctx, itemSlotFamilyGuardTestCharacterID); err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	} else if record.Slots["0:130"].ItemID != 3277 {
		t.Fatalf("unclassified family row = %+v", record.Slots)
	}
	if strings.Contains(readPeerEndpointTestLog(t, logger, logPath), itemSlotFamilyGuardMismatchEvent) {
		t.Fatal("unclassified family must not produce a guard event")
	}
}
