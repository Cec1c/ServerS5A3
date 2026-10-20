package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场故障（操作者原话）：「我把全身的防具穿脱后都显示耐久为0」，
// 并补充「身上装备的0耐久在进入副本后变为满 出图后也正常了」。
//
// 本文件是修复的钉子：**非 0 槽的"这一格是空的"必须写成哨兵 itemID=0xFFFFFFFF**，
// 不许写成 itemID=0（运行时 PVF 的 item 0 是 `stackable/gold.stk`「金币」）。
// 完整现场归因、105 行线上错误空行的来源与"为什么不改客户端"见
// empty_slot_row_encoding.go。

// 现场报文体（逐字节抄自 packet_log.txt，char 1 / game-000027 /
// t=2026-09-29T09:45:11.886042431Z，SEND msg_id=14，frame 138 B / body 122 B）：
// 解析结果 = list 0 / count 1 / slot 18 / itemID 0x00000000 / +9 之后全 0。
//
//	BODY_HEX=0001001200000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000
//
// 也就是"客户端刚把一件防具脱到 0:18，服务端把那一格回成 itemID=0 的空行"。
const liveWearSourceSlotEmptyRowBodyHex = "0001001200000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000"

// TestEmptySlotRowEncodingNormalizesUnsetToVerifiedSentinel 钉住开关默认值语义：
// 未设置/空/任意未知取值 = 修复档（哨兵），只有显式回退字面量才回到 itemID=0。
func TestEmptySlotRowEncodingNormalizesUnsetToVerifiedSentinel(t *testing.T) {
	cases := map[string]uint32{
		"":         emptySlotItemIDCurrentEXE,
		"   ":      emptySlotItemIDCurrentEXE,
		"sentinel": emptySlotItemIDCurrentEXE,
		"whatever": emptySlotItemIDCurrentEXE,
		"legacy":   emptySlotItemIDLegacy,
		"LEGACY":   emptySlotItemIDLegacy,
		" item0 ":  emptySlotItemIDLegacy,
		"zero":     emptySlotItemIDLegacy,
		"gold":     emptySlotItemIDLegacy,
		"item_0":   emptySlotItemIDLegacy,
	}
	for input, want := range cases {
		t.Setenv(emptySlotRowEncodingEnv, input)
		if got := emptySlotItemID(); got != want {
			t.Fatalf("emptySlotItemID() with %q = %#x, want %#x", input, got, want)
		}
		if got := emptySlotItemIDIsLegacy(); got != (want == emptySlotItemIDLegacy) {
			t.Fatalf("emptySlotItemIDIsLegacy() with %q = %t", input, got)
		}
	}
}

// TestWearSourceSlotEmptyRowUsesTheVerifiedSentinel 是本轮修复的正面钉子：
// 穿脱路径上"客户端指名的那个源槽"补的空行必须是哨兵，且整行除 slot 外全 0
// （与 inventory 包的 `writeEmptyCommonItemListEntry` 逐字节同形）。
func TestWearSourceSlotEmptyRowUsesTheVerifiedSentinel(t *testing.T) {
	t.Setenv(emptySlotRowEncodingEnv, "")
	service, session, conn := newEmptySlotRowTestService(t)

	if err := service.sendCurrentSocketInventorySlotUpdate(session, 0, 18, "test_source_slot"); err != nil {
		t.Fatalf("sendCurrentSocketInventorySlotUpdate error = %v", err)
	}
	body := singleCurrentItemUpdateBody(t, conn)

	if body[0] != 0 || binary.LittleEndian.Uint16(body[1:3]) != 1 {
		t.Fatalf("body header=%x, want list 0 with one row", body[:3])
	}
	if got := binary.LittleEndian.Uint16(body[3:5]); got != 18 {
		t.Fatalf("empty row slot=%d, want 18", got)
	}
	if got := binary.LittleEndian.Uint32(body[5:9]); got != emptySlotItemIDCurrentEXE {
		t.Fatalf("empty row itemID=%#x, want the verified sentinel %#x (itemID=0 is gold.stk)",
			got, emptySlotItemIDCurrentEXE)
	}
	if got := binary.LittleEndian.Uint32(body[9:13]); got != 0 {
		t.Fatalf("empty row amount=%d, want 0", got)
	}
	for offset := 13; offset < len(body); offset++ {
		if body[offset] != 0 {
			t.Fatalf("empty row byte +%#x = %#x, want 0 (row=%x)", offset, body[offset], body)
		}
	}
	// 整行 = slot + 哨兵 + 其余 0：逐字节断言"没有夹带任何残留字段"
	want := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(want[0:2], 18)
	binary.LittleEndian.PutUint32(want[2:6], emptySlotItemIDCurrentEXE)
	if got := body[3:]; !strings.EqualFold(hex.EncodeToString(got), hex.EncodeToString(want)) {
		t.Fatalf("empty row bytes=%x, want %x", got, want)
	}
}

// TestWearSourceSlotEmptyRowLegacyLiteralReproducesTodaysBytes 钉住一键回退：
// `DNFBRIDGE_EMPTY_SLOT_ROW_ENCODING=legacy` 必须逐字节复现今天线上那一包
// （现场报文原文见 liveWearSourceSlotEmptyRowBodyHex）。
func TestWearSourceSlotEmptyRowLegacyLiteralReproducesTodaysBytes(t *testing.T) {
	t.Setenv(emptySlotRowEncodingEnv, "legacy")
	service, session, conn := newEmptySlotRowTestService(t)

	if err := service.sendCurrentSocketInventorySlotUpdate(session, 0, 18, "test_source_slot"); err != nil {
		t.Fatalf("sendCurrentSocketInventorySlotUpdate error = %v", err)
	}
	body := singleCurrentItemUpdateBody(t, conn)
	if got := hex.EncodeToString(body); got != liveWearSourceSlotEmptyRowBodyHex {
		t.Fatalf("rollback body = %s\nwant the live bytes = %s", got, liveWearSourceSlotEmptyRowBodyHex)
	}
	if got := binary.LittleEndian.Uint32(body[5:9]); got != 0 {
		t.Fatalf("rollback empty row itemID=%#x, want today's 0", got)
	}
}

// TestWearSourceSlotPopulatedRowIsUntouchedByTheSwitch 钉住"有货的行一个字节都不动"：
// 源槽里真有东西时走的是权威 0x77 行（含 +0x0B..0x0C 耐久字），两档必须完全相同。
func TestWearSourceSlotPopulatedRowIsUntouchedByTheSwitch(t *testing.T) {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 18)
	binary.LittleEndian.PutUint32(raw[2:6], 100170519)
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 30)
	stack := dnfrepo.ItemStack{
		ItemID:   100170519,
		Count:    1,
		RawEntry: raw,
		Extra: map[string]string{
			"item_kind":      "equipment",
			"equipment_type": "[shoulder]",
			"pvf_path":       "equipment/character/common/shoulder/larmor/100170519.equ",
			"durability":     "30",
			"max_durability": "30",
		},
	}
	var sentinelBody, legacyBody []byte
	for _, tier := range []struct {
		env  string
		save *[]byte
	}{{"", &sentinelBody}, {"legacy", &legacyBody}} {
		t.Setenv(emptySlotRowEncodingEnv, tier.env)
		service, session, conn := newEmptySlotRowTestServiceWithStack(t, "0:18", stack)
		if err := service.sendCurrentSocketInventorySlotUpdate(session, 0, 18, "test_source_slot"); err != nil {
			t.Fatalf("sendCurrentSocketInventorySlotUpdate error = %v", err)
		}
		*tier.save = singleCurrentItemUpdateBody(t, conn)
	}
	if hex.EncodeToString(sentinelBody) != hex.EncodeToString(legacyBody) {
		t.Fatalf("populated row differs between tiers:\n sentinel=%s\n legacy  =%s",
			hex.EncodeToString(sentinelBody), hex.EncodeToString(legacyBody))
	}
	if got := binary.LittleEndian.Uint16(sentinelBody[3+0x0B : 3+0x0D]); got != 30 {
		t.Fatalf("populated row durability=%d, want the authoritative 30 (row=%x)", got, sentinelBody)
	}
	if got := binary.LittleEndian.Uint32(sentinelBody[5:9]); got != 100170519 {
		t.Fatalf("populated row itemID=%d, want 100170519", got)
	}
}

// TestGoldWalletRowKeepsItemZeroAndAmount 是操作者要求的反向钉子：
// **list 0 槽 0 的金币行必须仍然是 itemID=0**（它靠 amount 携带金币数，
// 与"空行"是两种语义），改动不许把它一起改成哨兵。
func TestGoldWalletRowKeepsItemZeroAndAmount(t *testing.T) {
	for _, env := range []string{"", "legacy"} {
		t.Setenv(emptySlotRowEncodingEnv, env)
		body := buildCurrentGoldStateBody(110852507)
		if body[0] != 0 || binary.LittleEndian.Uint16(body[1:3]) != 1 {
			t.Fatalf("gold body header=%x", body[:3])
		}
		if got := binary.LittleEndian.Uint16(body[3:5]); got != 0 {
			t.Fatalf("gold row slot=%d, want 0", got)
		}
		if got := binary.LittleEndian.Uint32(body[5:9]); got != 0 {
			t.Fatalf("gold row itemID=%#x, want 0 (the wallet row is not an empty slot)", got)
		}
		if got := binary.LittleEndian.Uint32(body[9:13]); got != 110852507 {
			t.Fatalf("gold row amount=%d, want 110852507", got)
		}
	}
}

// TestEpicProductionConsumedMaterialRowUsesTheSentinel 钉住另外两处同形空行
// （材料被扣光的那一格）也走同一条编码：它们在本份线上日志里是 0 行，
// 但 itemID=0 的语义错误与穿脱源槽完全相同。
func TestEpicProductionConsumedMaterialRowUsesTheSentinel(t *testing.T) {
	const slot int16 = 5
	inventory := &dnfrepo.InventoryRecord{
		Slots: map[string]dnfrepo.ItemStack{
			currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot): {
				ItemID: 3033,
				Count:  3,
				Extra:  map[string]string{"item_kind": "stackable"},
			},
		},
	}
	t.Setenv(emptySlotRowEncodingEnv, "")
	entry, err := currentEpicProductionConsumeStack(inventory, slot, 3033, 3)
	if err != nil {
		t.Fatalf("consume stack error = %v", err)
	}
	if got := binary.LittleEndian.Uint32(entry.data[2:6]); got != emptySlotItemIDCurrentEXE {
		t.Fatalf("consumed material empty row itemID=%#x, want the sentinel %#x", got, emptySlotItemIDCurrentEXE)
	}
	if _, still := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)]; still {
		t.Fatal("the consumed material slot must be gone from the repository")
	}

	t.Setenv(emptySlotRowEncodingEnv, "legacy")
	inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)] = dnfrepo.ItemStack{
		ItemID: 3033, Count: 3, Extra: map[string]string{"item_kind": "stackable"},
	}
	entry, err = currentEpicProductionConsumeStack(inventory, slot, 3033, 3)
	if err != nil {
		t.Fatalf("consume stack error (legacy) = %v", err)
	}
	if got := binary.LittleEndian.Uint32(entry.data[2:6]); got != 0 {
		t.Fatalf("legacy consumed material empty row itemID=%#x, want today's 0", got)
	}
}

// TestGuardianGemWarehouseEmptyRowUsesTheSentinel 钉住守护珠仓库格那一处同形空行。
func TestGuardianGemWarehouseEmptyRowUsesTheSentinel(t *testing.T) {
	t.Setenv(emptySlotRowEncodingEnv, "")
	service, session, conn := newEmptySlotRowTestService(t)
	if err := service.sendCurrentGuardianGemWarehouseSlotUpdate(session, 2, 7); err != nil {
		t.Fatalf("sendCurrentGuardianGemWarehouseSlotUpdate error = %v", err)
	}
	body := singleCurrentItemUpdateBody(t, conn)
	if got := binary.LittleEndian.Uint32(body[5:9]); got != emptySlotItemIDCurrentEXE {
		t.Fatalf("guardian gem warehouse empty row itemID=%#x, want the sentinel %#x", got, emptySlotItemIDCurrentEXE)
	}
	if got := binary.LittleEndian.Uint16(body[3:5]); got != 7 {
		t.Fatalf("guardian gem warehouse empty row slot=%d, want 7", got)
	}
}

func newEmptySlotRowTestService(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	return newEmptySlotRowTestServiceWithStack(t, "", dnfrepo.ItemStack{})
}

func newEmptySlotRowTestServiceWithStack(t *testing.T, key string, stack dnfrepo.ItemStack) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	slots := map[string]dnfrepo.ItemStack{}
	if key != "" {
		slots[key] = stack
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "21",
		Slots:       slots,
		Warehouse:   map[string]dnfrepo.ItemStack{},
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
		connID:                          "empty-slot-row-test",
		accountID:                       "dnf:1",
		channel:                         channelcatalog.Channel{ID: 21},
		selectedCharacterID:             21,
		initialTownRouteCharacterID:     21,
		initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
		townSceneReadyCharacterID:       21,
		connectionTownActorOwnerChannel: 21,
		townActorOwnerChannel:           21,
	}
	return service, session, connection
}

func singleCurrentItemUpdateBody(t *testing.T, conn *bufferConn) []byte {
	t.Helper()
	packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("slot refresh header=%+v, want the class0/op14 carrier", packet.Header)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing packets after the slot refresh: %x", rest)
	}
	return packet.Body
}
