package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住 `dungeon_passive_object_drop.go` 的两项改动：
//
//	① `DNFBRIDGE_MAP_OWNED_PASSIVE_OBJECT`：房图 `[special passive object]` 的注册表 +
//	   op39 map-owned 破坏事件的接收 + 掉落钩子（默认 legacy = 完全不接管）；
//	② `DNFBRIDGE_PASSIVE_OBJECT_DROP_ROW`：`probe` 档的一条 19 字节柱子掉落行 +
//	   掉落表登记（默认 legacy = extraCount 恒 0，逐字节等于今天）。
//
// 期望值有两个**真实锚点**，都不是自证：
//   - 现场帧：`/opt/dnf90/logs/packet_log.txt:73366`（pkt_seq=905，plain_body_len=254）
//     的 op29 body 原文，见 `currentDungeonHellRoomLiveStartMapBodyHex`；
//   - 改动前冻结的 fixture 帧：`currentDungeonHellRoomLegacyStartMapGoldenHex`
//     （在**未改动**的代码上跑出来的一次性快照，本文件只用它做回归钉）。

// currentDungeonHellRoomLiveStartMapBodyHex 是现场 op29 的 body（254 字节，去掉 16 字节头）。
//
// 逐字段（本文件的用例会解码并断言）：X=3 / Y=0 / layer=0 / seed=0x732c4a2f /
// HellPartyMode=1 / 0 / RoomState=1 / flag=1 / MapID=60083 / actors=11 /
// extraCount=0 / fog=1 / ridable=0 / party=0xff。
const currentDungeonHellRoomLiveStartMapBodyHex = "0300002f4a2c7301000100000001b3ea00000b000000000000ad01601501005a" +
	"0000000000000000000001000000ae01601501005a0000000000000000000002" +
	"000000af01601501005a0000000000000000000003000000b001621501005a00" +
	"00000000000000000004000000b101621501005a000000000000000000000500" +
	"0000b201621501005a0000000000000000010006000000b3011b0400005a0001" +
	"ff0000000000020007000000b401d10001005a0001ff00000000000200080000" +
	"00b501d20001005a0001ff0000000000020009000000b601d50001005a0001ff" +
	"000000000002000a000000b701d60001005a0001ff0000000000000100ff"

// currentDungeonHellRoomLegacyStartMapGoldenHex 是 fixture 深渊房间（`bridgeHellPartyPVF()`）
// 在**改动前**的 op29 body 快照：233 字节 = 19 + 10×21 + 4（10 个 actor，extraCount=0）。
// 任何把 probe 行为漏进默认档的改动都会让它不相等。
const currentDungeonHellRoomLegacyStartMapGoldenHex = "0000007856341201000100000001B3EA00000A0000000000009201601501005A0000000000000000000001000000930160150100" +
	"5A00000000000000000000020000009401601501005A00000000000000000000030000009501621501005A0000000000000000" +
	"0000040000009601621501005A00000000000000000000050000009701621501005A000000000000000001000600000098011A" +
	"0400005A0001FF00000000000200070000009901D10001005A0001FF00000000000200080000009A01D20001005A0001FF0000" +
	"0000000200090000009B01D30001005A0001FF0000000000000100FF"

func decodeCurrentDungeonHex(t *testing.T, value string) []byte {
	t.Helper()
	out, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return out
}

// liveHellRoomStartMapPacket 用工整的 11 行 actor 表重建现场那一帧（值全部来自上面那条
// 现场 body 的解码结果）。它是"编码器 vs 真实客户端流量"的对照物。
func liveHellRoomStartMapPacket() currentDungeonStartMap {
	actors := make([]currentDungeonStartMapActor, 0, 11)
	for index, code := range []uint32{71008, 71008, 71008, 71010, 71010, 71010} {
		actors = append(actors, currentDungeonStartMapActor{
			PacketIndex: uint32(index),
			ObjectKey:   uint16(429 + index),
			Code:        code,
			Level:       90,
		})
	}
	for index, wave := range []struct {
		order uint16
		code  uint32
	}{
		{order: 1, code: 1051},
		{order: 2, code: 65745},
		{order: 2, code: 65746},
		{order: 2, code: 65749},
		{order: 2, code: 65750},
	} {
		actors = append(actors, currentDungeonStartMapActor{
			TemplateOrder: wave.order,
			PacketIndex:   uint32(6 + index),
			ObjectKey:     uint16(435 + index),
			Code:          wave.code,
			Level:         90,
			Flag0:         hellPartyHiddenTemplateFlag,
			Flag1:         hellPartyAttachAllWavesSelector,
		})
	}
	return currentDungeonStartMap{
		X:                3,
		Y:                0,
		Seed:             0x732c4a2f,
		HellPartyMode:    1,
		RoomStateValue:   1,
		RoomStateFlag:    1,
		MapID:            60083,
		Actors:           actors,
		HellPartyFogFlag: 1,
		PartyMemberIndex: 0xff,
	}
}

// TestCurrentDungeonLiveHellRoomStartMapFrameDecodesToEmptyExtraTable 钉住现场帧本身：
// 11 行 actor（6 房图怪 + 5 波次隐藏行）、**extraCount = 0**、尾部 `00 01 00 ff`，
// 并且我们的编码器能逐字节重建它（⇒ 编码器在这一段与真实客户端流量一致）。
func TestCurrentDungeonLiveHellRoomStartMapFrameDecodesToEmptyExtraTable(t *testing.T) {
	live := decodeCurrentDungeonHex(t, currentDungeonHellRoomLiveStartMapBodyHex)
	if len(live) != 254 {
		t.Fatalf("live body len=%d want=254", len(live))
	}
	if live[0] != 3 || live[1] != 0 || live[2] != 0 ||
		binary.LittleEndian.Uint32(live[3:7]) != 0x732c4a2f ||
		live[7] != 1 || live[8] != 0 ||
		binary.LittleEndian.Uint32(live[9:13]) != 1 || live[13] != 1 ||
		binary.LittleEndian.Uint32(live[14:18]) != 60083 {
		t.Fatalf("live prefix=% X", live[:19])
	}
	actorCount := int(live[18])
	if actorCount != 11 {
		t.Fatalf("live actor count=%d want=11", actorCount)
	}
	extraOffset := 19 + actorCount*21
	if live[extraOffset] != 0 {
		t.Fatalf("live extraCount=%d want=0 (offset=%d)", live[extraOffset], extraOffset)
	}
	if tail := live[extraOffset:]; !bytes.Equal(tail, []byte{0x00, 0x01, 0x00, 0xff}) {
		t.Fatalf("live tail=% X want=00 01 00 FF", tail)
	}
	if bytes.Contains(live, []byte{0x6c, 0x02, 0x9b, 0x00}) {
		t.Fatal("live frame must not carry the probe item id 10158700")
	}

	built, err := liveHellRoomStartMapPacket().Build()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(built, live) {
		t.Fatalf("encoder does not reproduce the live frame:\n got=%X\nwant=%X", built, live)
	}
}

// The native u32 extra row is 20 bytes; the explicit legacy-u16 diagnostic
// row remains 19 bytes. Empty-room and actor rows retain their prior bytes.
func TestCurrentDungeonStartMapExtraEntryNative20AndLegacy19(t *testing.T) {
	base := liveHellRoomStartMapPacket()
	if built, err := base.Build(); err != nil {
		t.Fatal(err)
	} else if len(built) != 254 {
		t.Fatalf("baseline len=%d want=254", len(built))
	}
	probe := currentDungeonStartMapExtraEntry{
		PassiveObjectIndex: 0,
		GlobalSequence:     7,
		ItemID:             currentDungeonPassiveObjectProbeItemID,
		StackCount:         1,
	}

	// 厂商 19 字节（= `DNFBRIDGE_START_MAP_EXTRA_ENTRY_WIDTH=u16`，也是 probe 档的 auto 取值）。
	vendor := base
	vendor.ExtraEntries = []currentDungeonStartMapExtraEntry{probe}
	vendor.ExtraEntryVendorWidth = true
	vendorBody, err := vendor.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(vendorBody) != 273 {
		t.Fatalf("vendor-width body len=%d want=273", len(vendorBody))
	}
	wantRow := []byte{
		0x00,       // ObjectIndex
		0x07, 0x00, // GlobalSeq u16 LE
		0x6c, 0x02, 0x9b, 0x00, // 10158700
		0x01, 0x00, 0x00, 0x00, // stack 1
		0x00, 0x00, // endurance
		0x00,       // amplify type
		0x00, 0x00, // amplify value
		0x00, 0x00, // extended u16
		0x00,             // extended u8
		0x01, 0x00, 0xff, // fog / ridable / party
	}
	// extraCount 字节在 `19 + 11×21`（这一字节本来就存在），行紧跟其后。
	extraCountOffset := 19 + 11*21
	if tail := vendorBody[extraCountOffset+1:]; !bytes.Equal(tail, wantRow) {
		t.Fatalf("vendor-width tail=% X\n               want=% X", tail, wantRow)
	}
	if vendorBody[extraCountOffset] != 1 {
		t.Fatalf("vendor-width extraCount=%d want=1", vendorBody[extraCountOffset])
	}

	// 原生20字节（= 显式 `u32`）：同样一条行，只是 GlobalSequence 宽度不同。
	current := base
	current.ExtraEntries = []currentDungeonStartMapExtraEntry{probe}
	current.ExtraEntryVendorWidth = false
	currentBody, err := current.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(currentBody) != 274 {
		t.Fatalf("current-width body len=%d want=274", len(currentBody))
	}
	rowStart := extraCountOffset + 1
	if currentBody[rowStart] != 0x00 || currentBody[rowStart+1] != 0x07 ||
		currentBody[rowStart+2] != 0x00 || currentBody[rowStart+3] != 0x00 ||
		currentBody[rowStart+5] != 0x6c {
		t.Fatalf("current-width row prefix=% X (item id must sit at +5, u32 slot at +1)", currentBody[rowStart:rowStart+12])
	}
}

// TestDungeonPassiveObjectDropSwitchPolicy 钉三个开关的字面量与默认档：任何未列出的取值
// 都必须落回"今天"那一档。
func TestDungeonPassiveObjectDropSwitchPolicy(t *testing.T) {
	rowCases := []struct {
		raw  string
		want passiveObjectDropRowMode
	}{
		{raw: "", want: passiveObjectDropRowLegacy},
		{raw: "legacy", want: passiveObjectDropRowLegacy},
		{raw: "  ", want: passiveObjectDropRowLegacy},
		{raw: "PROBE", want: passiveObjectDropRowProbe},
		{raw: " probe ", want: passiveObjectDropRowProbe},
		{raw: "on", want: passiveObjectDropRowLegacy},
		{raw: "1", want: passiveObjectDropRowLegacy},
		{raw: "vendor", want: passiveObjectDropRowLegacy},
	}
	for _, testCase := range rowCases {
		if got := readDungeonPassiveObjectDropRowMode(testCase.raw); got != testCase.want {
			t.Fatalf("DNFBRIDGE_PASSIVE_OBJECT_DROP_ROW=%q => %v want=%v", testCase.raw, got, testCase.want)
		}
	}
	widthCases := []struct {
		raw  string
		want startMapExtraEntryWidth
	}{
		{raw: "", want: startMapExtraEntryWidthAuto},
		{raw: "auto", want: startMapExtraEntryWidthAuto},
		{raw: "legacy", want: startMapExtraEntryWidthAuto},
		{raw: "u16", want: startMapExtraEntryWidthVendor},
		{raw: "VENDOR", want: startMapExtraEntryWidthVendor},
		{raw: "u32", want: startMapExtraEntryWidthCurrent},
		{raw: "current", want: startMapExtraEntryWidthCurrent},
	}
	for _, testCase := range widthCases {
		if got := readDungeonStartMapExtraEntryWidth(testCase.raw); got != testCase.want {
			t.Fatalf("DNFBRIDGE_START_MAP_EXTRA_ENTRY_WIDTH=%q => %v want=%v", testCase.raw, got, testCase.want)
		}
	}
	// auto 的关键语义：没有行时不变（今天），有行时取厂商 19 字节。
	if startMapExtraEntryWidthAuto.vendorLayout(false) || !startMapExtraEntryWidthAuto.vendorLayout(true) {
		t.Fatal("auto width must stay current-without-a-row and vendor-with-a-row")
	}
	if startMapExtraEntryWidthCurrent.vendorLayout(true) {
		t.Fatal("explicit u32 selects the native 20-byte row")
	}
	if !startMapExtraEntryWidthVendor.vendorLayout(false) {
		t.Fatal("explicit u16 must be vendor width even without a row")
	}
	mapOwnedCases := []struct {
		raw  string
		want mapOwnedPassiveObjectMode
	}{
		{raw: "", want: mapOwnedPassiveObjectLegacy},
		{raw: "legacy", want: mapOwnedPassiveObjectLegacy},
		{raw: "off", want: mapOwnedPassiveObjectLegacy},
		{raw: "on", want: mapOwnedPassiveObjectLegacy},
		{raw: "register", want: mapOwnedPassiveObjectRegister},
		{raw: " REGISTER ", want: mapOwnedPassiveObjectRegister},
	}
	for _, testCase := range mapOwnedCases {
		if got := readDungeonMapOwnedPassiveObjectMode(testCase.raw); got != testCase.want {
			t.Fatalf("DNFBRIDGE_MAP_OWNED_PASSIVE_OBJECT=%q => %v want=%v", testCase.raw, got, testCase.want)
		}
	}
}

// bridgeHellPartyProbeDropPVF 在深渊 fixture 上补一条真实形状的 stackable 物品
// （`10158700`）——probe 档**只在真 PVF 解析得到该物品时才发行**，所以用例必须提供它。
func bridgeHellPartyProbeDropPVF() bridgePVFSource {
	source := bridgeHellPartyPVF()
	source[dungeonDropStackableList] = "10158700 `hell_probe.stk`\n"
	source["stackable/hell_probe.stk"] = "[name]\n`Hell Probe Drop`\n[stackable type]\n`[material]`\n" +
		"[stack limit]\n999\n[attach type]\n`[free]`\n"
	// `newPVFDungeonDropCatalog` 三条表都要在（与 `bridgeDungeonPVF` 同形）。
	source[dungeonDropEquipmentList] = "9001 `weapon/hell_probe.equ`\n"
	source["equipment/weapon/hell_probe.equ"] = "[name]\n`Hell Probe Equipment`\n[durability]\n50\n[attach type]\n`[trade]`\n"
	return source
}

// TestDungeonHellRoomLegacyTierIsByteIdenticalToToday 是默认档的反向断言：
// `legacy` 下深渊房间的 op29 逐字节等于改动前的快照，extraCount 恒 0。
func TestDungeonHellRoomLegacyTierIsByteIdenticalToToday(t *testing.T) {
	t.Setenv(passiveObjectDropRowEnv, "")
	t.Setenv(startMapExtraEntryWidthEnv, "")
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "")
	service, _, _, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyProbeDropPVF())
	if err := service.prepareCurrentDungeonPassiveObjectDrops(nil, runtime, scene, "test_legacy"); err != nil {
		t.Fatal(err)
	}
	if runtime.MapOwnedPassiveObjects != nil {
		t.Fatalf("legacy tier must not build the map-owned table: %+v", runtime.MapOwnedPassiveObjects)
	}
	if runtime.DropOwner != nil {
		t.Fatal("legacy tier must not register any drop")
	}
	packets, err := service.buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		t.Fatal(err)
	}
	golden := decodeCurrentDungeonHex(t, currentDungeonHellRoomLegacyStartMapGoldenHex)
	if !bytes.Equal(packets.StartMap, golden) {
		t.Fatalf("legacy op29 body changed:\n got=%X\nwant=%X", packets.StartMap, golden)
	}
	actorCount := int(packets.StartMap[18])
	if actorCount != 10 {
		t.Fatalf("fixture actor count=%d want=10", actorCount)
	}
	if packets.StartMap[19+actorCount*21] != 0 {
		t.Fatalf("extraCount=%d want=0", packets.StartMap[19+actorCount*21])
	}
}

// TestDungeonHellRoomProbeTierEmitsOneVendorRowAndRegistersTheDrop 是 probe 档的主用例：
// 恰好一条 19 字节行（厂商布局）、body +19 字节、字节逐位可判、并且这条行同时进了掉落表
// （`SceneSlot = GlobalSeq`，与厂商 `PassiveObjectDropProjectionService.cs:122-140` 同义）。
func TestDungeonHellRoomProbeTierEmitsOneVendorRowAndRegistersTheDrop(t *testing.T) {
	t.Setenv(passiveObjectDropRowEnv, "probe")
	t.Setenv(startMapExtraEntryWidthEnv, "")
	service, _, _, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyProbeDropPVF())
	if err := service.prepareCurrentDungeonPassiveObjectDrops(nil, runtime, scene, "test_probe"); err != nil {
		t.Fatal(err)
	}
	registration := runtime.currentDungeonMapOwnedPassiveObjectRegistration(scene)
	if registration == nil || len(registration.Objects) != 1 {
		t.Fatalf("map-owned registration=%+v", registration)
	}
	pillar := registration.Objects[0]
	if pillar.ObjectIndex != 0 || pillar.ObjectCode != 30528 || pillar.HellPartyEntries != 3 || pillar.SpawnCount != 0 {
		t.Fatalf("pillar row=%+v want index=0 code=30528 hellparty=3 spawns=0", pillar)
	}
	if len(registration.Rows) != 1 {
		t.Fatalf("projected rows=%d want=1", len(registration.Rows))
	}
	row := registration.Rows[0]
	if !registration.VendorWidth {
		t.Fatal("probe tier with auto width must write the vendor 19-byte layout")
	}
	if row.ObjectIndex != 0 || row.ItemID != currentDungeonPassiveObjectProbeItemID || row.StackCount != 1 {
		t.Fatalf("projected row=%+v", row)
	}
	// 掉落表登记：同一份 `SceneSlot`/objectKey 必须能在账本里查到（否则客户端落地上也拾取不了）。
	if runtime.DropOwner == nil {
		t.Fatal("probe tier must register the drop in the room drop table")
	}
	registered := runtime.DropOwner.mapOwnedPassiveObjectDrop(row.SceneSlot)
	if registered == nil || registered.ObjectKey != row.ObjectKey ||
		registered.Item.ItemID != currentDungeonPassiveObjectProbeItemID ||
		registered.SceneSlot != row.SceneSlot || registered.Amount != 1 ||
		registered.Room != runtimeDungeonRoomKeyFromScene(scene) ||
		registered.Status != runtimeDungeonDropAvailable {
		t.Fatalf("registered drop=%+v row=%+v", registered, row)
	}

	packets, err := service.buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		t.Fatal(err)
	}
	body := packets.StartMap
	if len(body) != 252 {
		t.Fatalf("probe op29 len=%d want=252 (fixture 233 + 19)", len(body))
	}
	actorCount := int(body[18])
	extraOffset := 19 + actorCount*21
	if body[extraOffset] != 1 {
		t.Fatalf("extraCount=%d want=1", body[extraOffset])
	}
	wantRow := make([]byte, 0, 22)
	wantRow = append(wantRow, 0x00)
	wantRow = binary.LittleEndian.AppendUint16(wantRow, row.SceneSlot)
	wantRow = binary.LittleEndian.AppendUint32(wantRow, currentDungeonPassiveObjectProbeItemID)
	wantRow = binary.LittleEndian.AppendUint32(wantRow, 1)
	wantRow = append(wantRow, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	// fog / ridable 组数 / party 索引（本帧的既有值，逐字节与默认档同）。
	wantRow = append(wantRow, 0x01, 0x00, 0xff)
	if got := body[extraOffset+1:]; !bytes.Equal(got, wantRow) {
		t.Fatalf("probe tail=% X\n       want=% X", got, wantRow)
	}
	if !bytes.Contains(body, []byte{0x6c, 0x02, 0x9b, 0x00}) {
		t.Fatal("probe row must carry item id 10158700 little-endian")
	}
	// 与"只差这一条行"的强断言：默认档 body 的长度 + 19、且前缀与 actor 表逐字节相同，
	// 只有 extraCount 字节 0→1 并按厂商 19 字节布局补上那一行。
	legacy := decodeCurrentDungeonHex(t, currentDungeonHellRoomLegacyStartMapGoldenHex)
	if len(body) != len(legacy)+19 || !bytes.Equal(body[:extraOffset], legacy[:extraOffset]) ||
		body[extraOffset] != 1 || legacy[extraOffset] != 0 {
		t.Fatalf("probe tier must differ from today only by the extra table:\n got_len=%d want_len=%d\n got_prefix=%X\n legacy_prefix=%X",
			len(body), len(legacy)+19, body[:extraOffset], legacy[:extraOffset])
	}

	// 幂等：再准备一次（barrier 前/后各一次的真实形状）不得发第二条行、也不得换 slot。
	before := append([]byte(nil), body...)
	if err := service.prepareCurrentDungeonPassiveObjectDrops(nil, runtime, scene, "test_probe_again"); err != nil {
		t.Fatal(err)
	}
	if len(registration.Rows) != 1 {
		t.Fatalf("re-preparing the same room must reuse the row, rows=%d", len(registration.Rows))
	}
	again, err := service.buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.StartMap, before) {
		t.Fatalf("rebuilt probe frame changed:\n got=%X\nwant=%X", again.StartMap, before)
	}
}

// TestDungeonPassiveObjectDropRowIsBlockedWhenTheItemIsNotInTheRealPVF 钉住"宁可不发"：
// 物品在真 PVF 里解析不出来时，一行都不发、一个字节都不变。
func TestDungeonPassiveObjectDropRowIsBlockedWhenTheItemIsNotInTheRealPVF(t *testing.T) {
	t.Setenv(passiveObjectDropRowEnv, "probe")
	service, _, _, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
	if err := service.prepareCurrentDungeonPassiveObjectDrops(nil, runtime, scene, "test_probe_unresolved"); err != nil {
		t.Fatal(err)
	}
	registration := runtime.currentDungeonMapOwnedPassiveObjectRegistration(scene)
	if registration == nil {
		t.Fatal("the map-owned registration itself must still exist (it is independent of the row)")
	}
	if len(registration.Rows) != 0 || runtime.DropOwner != nil {
		t.Fatalf("unresolved item must not project a row: rows=%+v owner=%+v", registration.Rows, runtime.DropOwner)
	}
	packets, err := service.buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packets.StartMap, decodeCurrentDungeonHex(t, currentDungeonHellRoomLegacyStartMapGoldenHex)) {
		t.Fatalf("blocked probe row must leave the frame byte-identical: %X", packets.StartMap)
	}
}

// TestDungeonProbeRowIsScopedToTheHellRoom 钉住作用域：非深渊房间既不注册表也不发行。
func TestDungeonProbeRowIsScopedToTheHellRoom(t *testing.T) {
	t.Setenv(passiveObjectDropRowEnv, "probe")
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "register")
	service, _, _, runtime, _ := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyProbeDropPVF())
	plain := worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 100}},
	}
	if err := service.prepareCurrentDungeonPassiveObjectDrops(nil, runtime, plain, "test_plain_room"); err != nil {
		t.Fatal(err)
	}
	if runtime.currentDungeonMapOwnedPassiveObjectRegistration(plain) != nil {
		t.Fatal("a map without [special passive object] rows must not register an empty table")
	}
	if runtime.DropOwner != nil {
		t.Fatal("a non-hell room must not project a drop row")
	}
}

// mapOwnedDeathRequest 造一条"柱子被打破"形状的 op39（厂商 `DieMonsterRequest.Parse`）：
// localIndex(u16) = 对象 code、body[4:6] = 0xFFFF 的 map-owned 签名。
func mapOwnedDeathRequest(objectCode uint16) dungeoncmd.DieMonsterRequest {
	return dungeoncmd.DieMonsterRequest{
		Layout:           dungeoncmd.DieMonsterRequestLayoutFixed62,
		RuntimeObjectKey: uint32(objectCode),
		OwnerObjectKey:   math.MaxUint16,
	}
}

func mapOwnedDeathBody(objectCode uint32) []byte {
	body := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], objectCode)
	binary.LittleEndian.PutUint16(body[4:6], math.MaxUint16)
	return body
}

// TestMapOwnedPassiveObjectBreakIsInertByDefault 是 ① 的默认档反向断言：
// `legacy` 下 op39 的 map-owned 分支**完全不接管**（一行日志、一次状态都没有）。
func TestMapOwnedPassiveObjectBreakIsInertByDefault(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "")
	t.Setenv(passiveObjectDropRowEnv, "")
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyProbeDropPVF())
	service.packetLog = logger
	if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "test_inert"); err != nil {
		t.Fatal(err)
	}
	handled, err := service.handleCurrentDungeonMapOwnedPassiveObjectDeathLocked(
		session, runtime, scene, mapOwnedDeathRequest(30528))
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("legacy tier must not consume the map-owned passive-object break")
	}
	if runtime.MapOwnedPassiveObjects != nil {
		t.Fatalf("legacy tier must not build the map-owned table: %+v", runtime.MapOwnedPassiveObjects)
	}
	if err := service.handleDungeonMonsterDeath(session, mapOwnedDeathBody(30528)); err != nil {
		t.Fatal(err)
	}
	if len(conn.write.Bytes()) != 0 {
		t.Fatalf("legacy tier must not answer the pillar break: %X", conn.write.Bytes())
	}
	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	if strings.Contains(logged, "game-dungeon-map-owned-passive-object-destroyed") {
		t.Fatalf("legacy tier must not log the map-owned branch:\n%s", logged)
	}
}

// TestMapOwnedPassiveObjectBreakReachesTheDropHookWhenRegistered 是 ① 的主用例：
// `register`（+ probe）下，柱子的 op39 被收下、记进本房间台账（下标键）、**不回 op38**、
// 并把已投影的那条掉落行标成已释放；重复上报是幂等忽略。
func TestMapOwnedPassiveObjectBreakReachesTheDropHookWhenRegistered(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "register")
	t.Setenv(passiveObjectDropRowEnv, "probe")
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyProbeDropPVF())
	service.packetLog = logger
	if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "test_register"); err != nil {
		t.Fatal(err)
	}
	registration := runtime.currentDungeonMapOwnedPassiveObjectRegistration(scene)
	if registration == nil || len(registration.Rows) != 1 {
		t.Fatalf("registration=%+v", registration)
	}
	row := registration.Rows[0]

	// 公告过的 actor 序号必须继续走既有路径（厂商 ShouldTreatAsPassiveObject:182-183）。
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	announcedKey := uint16(0)
	if snapshot := runtime.Room.Snapshot(); len(snapshot.Monsters) > 0 {
		announcedKey = uint16(snapshot.Monsters[0].ObjectKey)
	}
	if announcedKey == 0 {
		t.Fatal("fixture must announce at least one room actor")
	}
	handled, err := service.handleCurrentDungeonMapOwnedPassiveObjectDeathLocked(
		session, runtime, scene, mapOwnedDeathRequest(announcedKey))
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatalf("announced actor key %d must not be taken over by the map-owned branch", announcedKey)
	}

	handled, err = service.handleCurrentDungeonMapOwnedPassiveObjectDeathLocked(
		session, runtime, scene, mapOwnedDeathRequest(30528))
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("registered tier must consume the pillar break")
	}
	if !registration.Deaths[0] {
		t.Fatalf("the break must be recorded under the map list index: deaths=%+v", registration.Deaths)
	}
	if !row.Released {
		t.Fatal("the projected drop row must be released by the break")
	}
	if len(conn.write.Bytes()) != 0 {
		t.Fatalf("the map-owned branch must not send op38: %X", conn.write.Bytes())
	}
	// 重复上报：幂等，不改状态。
	if err := service.handleDungeonMonsterDeath(session, mapOwnedDeathBody(30528)); err != nil {
		t.Fatal(err)
	}
	if len(conn.write.Bytes()) != 0 {
		t.Fatalf("duplicate pillar break must not be answered: %X", conn.write.Bytes())
	}
	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	for _, want := range []string{
		"game-dungeon-passive-object-drop-row-registered",
		"game-dungeon-map-owned-passive-object-destroyed",
		"game-dungeon-map-owned-passive-object-drop-released",
		"game-dungeon-map-owned-passive-object-duplicate",
		"death_ack_sent=false",
		"object_index=0",
		"object_id=30528",
		fmt.Sprintf("scene_slot=%d", row.SceneSlot),
		"item_id=10158700",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("packet log is missing %q:\n%s", want, logged)
		}
	}
}

// TestMapOwnedPassiveObjectBreakWithoutARowIsLoggedAsAbsent 是 A/B 的判别面：
// 只开 ①（不发 probe 行）时，破坏事件照样被收下并记账，但日志必须写
// `drop-absent`（= "事件到了、这一档没有行"），而不是静默。
func TestMapOwnedPassiveObjectBreakWithoutARowIsLoggedAsAbsent(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "register")
	t.Setenv(passiveObjectDropRowEnv, "")
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, session, _, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyProbeDropPVF())
	service.packetLog = logger
	if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "test_register_only"); err != nil {
		t.Fatal(err)
	}
	if runtime.DropOwner != nil {
		t.Fatal("register-only tier must not project any drop row")
	}
	handled, err := service.handleCurrentDungeonMapOwnedPassiveObjectDeathLocked(
		session, runtime, scene, mapOwnedDeathRequest(30528))
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("register-only tier must still consume the pillar break")
	}
	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	if !strings.Contains(logged, "game-dungeon-map-owned-passive-object-drop-absent") {
		t.Fatalf("register-only tier must log drop-absent:\n%s", logged)
	}
	if strings.Contains(logged, "game-dungeon-map-owned-passive-object-drop-released") {
		t.Fatalf("register-only tier must not report a released drop:\n%s", logged)
	}
}

// TestPickupFindsThePreGeneratedPassiveObjectDropBySceneSlot 钉住"登记的另一半"：
// 客户端只可能拿 scene slot 来拾取，回退查找必须命中；而普通掉落不会被 slot 回退命中。
func TestPickupFindsThePreGeneratedPassiveObjectDropBySceneSlot(t *testing.T) {
	owner := newRuntimeDungeonDropOwner()
	room := runtimeDungeonRoomKey{X: 3, Y: 0, MapID: 60083}
	drop := &runtimeDungeonDrop{
		ObjectKey:             900,
		SceneSlot:             7,
		Item:                  dungeonDropItemDefinition{ItemID: currentDungeonPassiveObjectProbeItemID, Kind: dungeonDropItemStackable},
		Amount:                1,
		Room:                  room,
		Status:                runtimeDungeonDropAvailable,
		MapOwnedPassiveObject: true,
		MapOwnedPassiveIndex:  0,
	}
	if err := owner.registerBatch([]*runtimeDungeonDrop{drop}, 8); err != nil {
		t.Fatal(err)
	}
	found, err := owner.ownedMapOwnedPassiveObjectBySceneSlot(7, room)
	if err != nil || found != drop {
		t.Fatalf("scene-slot fallback found=%+v err=%v", found, err)
	}
	if _, err := owner.ownedMapOwnedPassiveObjectBySceneSlot(7, runtimeDungeonRoomKey{X: 0, Y: 0, MapID: 100}); err == nil {
		t.Fatal("scene-slot fallback must reject another room")
	}
	// 普通击杀掉落不进 slot 索引 ⇒ 回退查找永远找不到它（默认档行为不变）。
	ordinary := &runtimeDungeonDrop{ObjectKey: 901, SceneSlot: 8, Room: room, Status: runtimeDungeonDropAvailable}
	if err := owner.registerBatch([]*runtimeDungeonDrop{ordinary}, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ownedMapOwnedPassiveObjectBySceneSlot(8, room); err == nil {
		t.Fatal("an ordinary drop must never be reachable through the scene-slot fallback")
	}
}

// TestMapOwnedPassiveObjectRegistrationIsKeyedByMapListIndex 钉住键序：厂商
// `DungeonInstance.cs:382-404` 按列表下标记账、按 code 取第一个命中。
func TestMapOwnedPassiveObjectRegistrationIsKeyedByMapListIndex(t *testing.T) {
	scene := worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 1, Y: 2},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 60083}},
		SpecialPassiveObjects: []worldmap.SpecialPassiveObject{
			{PassiveObject: worldmap.PassiveObject{ObjectID: 30528}, HellParty: []worldmap.HellPartyEntry{{GroupID: 125, Rate: 25, Order: 1}}},
			{PassiveObject: worldmap.PassiveObject{ObjectID: 40001}, Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[monster]", Code: 1}}},
		},
	}
	runtime := &runtimeDungeonState{}
	registration := runtime.registerCurrentDungeonMapOwnedPassiveObjects(scene)
	if registration == nil || len(registration.Objects) != 2 {
		t.Fatalf("registration=%+v", registration)
	}
	if registration.Objects[0].ObjectIndex != 0 || registration.Objects[0].ObjectCode != 30528 ||
		registration.Objects[0].HellPartyEntries != 1 || registration.Objects[0].SpawnCount != 0 {
		t.Fatalf("object[0]=%+v", registration.Objects[0])
	}
	if registration.Objects[1].ObjectIndex != 1 || registration.Objects[1].ObjectCode != 40001 ||
		registration.Objects[1].SpawnCount != 1 {
		t.Fatalf("object[1]=%+v", registration.Objects[1])
	}
	if pillar, ok := registration.hellPillarObject(); !ok || pillar.ObjectIndex != 0 {
		t.Fatalf("hell pillar=%+v ok=%t", pillar, ok)
	}
	if got, found := registration.objectByCode(30528); !found || got.ObjectIndex != 0 {
		t.Fatalf("objectByCode(30528)=%+v found=%t", got, found)
	}
	if got, found := registration.objectByCode(40001); !found || got.ObjectIndex != 1 {
		t.Fatalf("objectByCode(40001)=%+v found=%t", got, found)
	}
	if _, found := registration.objectByCode(99999); found {
		t.Fatal("objectByCode must not match a code that is not in the room map")
	}
	// 同一房间重复注册必须是同一张表（幂等），不重置台账。
	registration.Deaths[0] = true
	if again := runtime.registerCurrentDungeonMapOwnedPassiveObjects(scene); again != registration || !again.Deaths[0] {
		t.Fatal("re-registering the same room must reuse the ledger")
	}
}

// TestHellPillarObjectRequiresExactlyOneHellPartyRow 钉住"不猜"：没有 `[hellparty]` 行或
// 有多行时，一行都不发。
func TestHellPillarObjectRequiresExactlyOneHellPartyRow(t *testing.T) {
	noHellParty := &currentDungeonMapOwnedPassiveObjectRegistration{
		Objects: []currentDungeonMapOwnedPassiveObject{{ObjectIndex: 0, ObjectCode: 30528, SpawnCount: 1}},
	}
	if _, ok := noHellParty.hellPillarObject(); ok {
		t.Fatal("a map without an inline [hellparty] row must not be treated as a pillar")
	}
	twoPillars := &currentDungeonMapOwnedPassiveObjectRegistration{
		Objects: []currentDungeonMapOwnedPassiveObject{
			{ObjectIndex: 0, ObjectCode: 30528, HellPartyEntries: 1},
			{ObjectIndex: 1, ObjectCode: 30529, HellPartyEntries: 2},
		},
	}
	if _, ok := twoPillars.hellPillarObject(); ok {
		t.Fatal("two [hellparty] rows are ambiguous and must not produce a row")
	}
}

// testPassiveObjectDropPacketLogger 与其它用例同形的临时包日志（`EVENT kind=…` 行）。
// `t.Cleanup` 兜底关闭：用例中途 Fatal 时临时目录仍能被回收。
func testPassiveObjectDropPacketLogger(t *testing.T) (*packetLogger, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "passive-object-drop-packet-log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, logPath
}

func closePassiveObjectDropPacketLogger(t *testing.T, logger *packetLogger, logPath string) string {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
