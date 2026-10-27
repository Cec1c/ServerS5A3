package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件是操作者清单第 56 条「NPC 红尾乔纳森未出现」的服务端实验阀的聚焦用例。
//
// 取证（真档 `Script.pvf`，sha256 cc517a6e…，全档 1,057,625 个条目）：
//   - NPC 476 = `npc/476_jonathan.npc`（`[name]` / `[field name]` = 红尾乔纳森），
//     唯一落点是 `map/town/centralpark/centralpark_metrocenter.map` 的 `[NPC]`
//     `476 \`[right]\` 873 200 0`，即**中央公园（town 31）3 区 地铁中心**；
//   - 该文档唯一的可见性闸门是 `[open visible event] 2395`（第 60-61 行），文档里
//     **没有** `[event open]` 段；
//   - `[open visible event]` 在全档只出现在 `.npc` 文档（203 处），取值为 2395 的
//     只有 476 这一个；
//   - 我们的 op108 在线活动目录只带 2347（+受开关控制的马战 2365），从不带 2395。
//
// 三条判据：
//  1. 开关关（默认/任何未识别值）⇒ op108 与改动前**逐字节相同**（含 2365 两档），
//     既有 golden 一个字节都不动；
//  2. 开关开 ⇒ 只多出 2395 的 base+active 两行，2347/2365 的字节逐字节不变，
//     两个计数与实际行数自洽（逐字段解析回读，不是 grep 字节）；
//  3. 新 env 的取值口径与工程其它实验阀一致：只有显式打开值才算开。

// townNpcEventCatalogOnBody == joust-on body + 2395 两行。这两个常量是**新增**的
// 期望值（不覆盖任何既有 golden）：base 行 20 字节（u16 id + u32 value + 两个空 dstr
// + has_extended=0），active 行 50 字节（u16 id + 12×u32 0）。
const (
	currentTownNpcEventBaseRowHex   = "5b0901000000000000000000000000"
	currentTownNpcEventActiveRowHex = "5b09000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
)

func mustHexBytes(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode %q: %v", value, err)
	}
	return data
}

// TestTownNpcEventCatalogOffIsByteIdenticalToToday 钉住默认档：关着的时候，
// 4 参数构造器必须与历史 3 参数构造器逐字节相同（马战开/关两档都比）。
func TestTownNpcEventCatalogOffIsByteIdenticalToToday(t *testing.T) {
	for _, includeJoust := range []bool{false, true} {
		historical, err := buildCurrentSpendTimeEventInfoBody(
			currentSpendTimeJoustCatalogGoldenRewards, 10800, includeJoust)
		if err != nil {
			t.Fatal(err)
		}
		extended, err := buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent(
			currentSpendTimeJoustCatalogGoldenRewards, 10800, includeJoust, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(extended, historical) {
			t.Fatalf("town-npc-catalog off (joust=%t) body=%x want byte-identical %x",
				includeJoust, extended, historical)
		}
		if includeJoust {
			if golden := mustCurrentSpendTimeJoustCatalogGolden(t); !bytes.Equal(extended, golden) {
				t.Fatalf("town-npc-catalog off dropped the joust golden: %x want %x", extended, golden)
			}
		}
		shape := parseCurrentSpendTimeEventInfoBody(t, extended)
		if shape.hasEvent(uint16(currentTownNpcVisibleEventID)) {
			t.Fatalf("off-catalog still carries event %d", currentTownNpcVisibleEventID)
		}
	}
}

// TestTownNpcEventCatalogOnAddsOnlyThe2395Pair 钉住打开档：只多 2395 一对行，
// 2347 与 2365 的字节逐字节不变，两个计数与实际行数自洽。
func TestTownNpcEventCatalogOnAddsOnlyThe2395Pair(t *testing.T) {
	golden := parseCurrentSpendTimeEventInfoBody(t, mustCurrentSpendTimeJoustCatalogGolden(t))

	body, err := buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent(
		currentSpendTimeJoustCatalogGoldenRewards, 10800, true, true)
	if err != nil {
		t.Fatal(err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	if shape.baseCount != 3 || shape.activeCount != 3 ||
		len(shape.baseRows) != 3 || len(shape.activeRows) != 3 {
		t.Fatalf("on-catalog counts base=%d/%d active=%d/%d want 3/3 and 3/3",
			shape.baseCount, len(shape.baseRows), shape.activeCount, len(shape.activeRows))
	}
	// 前两行必须是 golden 里的 2347 / 2365，逐字节。
	if !bytes.Equal(shape.baseRows[0].raw, golden.baseRows[0].raw) {
		t.Fatalf("2347 base row changed: %x want %x", shape.baseRows[0].raw, golden.baseRows[0].raw)
	}
	if !bytes.Equal(shape.baseRows[1].raw, golden.baseRows[1].raw) {
		t.Fatalf("2365 base row changed: %x want %x", shape.baseRows[1].raw, golden.baseRows[1].raw)
	}
	if !bytes.Equal(shape.activeRows[0].raw, golden.activeRows[0].raw) {
		t.Fatalf("2347 active row changed: %x want %x", shape.activeRows[0].raw, golden.activeRows[0].raw)
	}
	if !bytes.Equal(shape.activeRows[1].raw, golden.activeRows[1].raw) {
		t.Fatalf("2365 active row changed: %x want %x", shape.activeRows[1].raw, golden.activeRows[1].raw)
	}
	// 多出来的两行必须是 2395，且形状与 2365 完全同构（无时间窗、无点击动作）。
	if got, want := shape.baseRows[2].raw, mustHexBytes(t, currentTownNpcEventBaseRowHex); !bytes.Equal(got, want) {
		t.Fatalf("2395 base row=%x want %x", got, want)
	}
	if shape.baseRows[2].eventID != uint16(currentTownNpcVisibleEventID) ||
		shape.baseRows[2].value != currentSpendTimeBaseCatalogValue || shape.baseRows[2].hasExtended {
		t.Fatalf("2395 base row parsed=%+v", shape.baseRows[2])
	}
	if got, want := shape.activeRows[2].raw, mustHexBytes(t, currentTownNpcEventActiveRowHex); !bytes.Equal(got, want) {
		t.Fatalf("2395 active row=%x want %x", got, want)
	}
	if shape.activeRows[2].eventID != uint16(currentTownNpcVisibleEventID) {
		t.Fatalf("2395 active row event=%d", shape.activeRows[2].eventID)
	}
	for param, value := range shape.activeRows[2].params {
		if value != 0 {
			t.Fatalf("2395 active row param[%d]=%d want 0", param, value)
		}
	}
}

// TestTownNpcEventCatalogOnWithoutJoustKeepsCountsConsistent 钉住"马战关、2395 开"
// 的组合：目录里只有 2347 + 2395，计数 2/2，且 2347 两行仍逐字节不变。
func TestTownNpcEventCatalogOnWithoutJoustKeepsCountsConsistent(t *testing.T) {
	body, err := buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent(
		currentSpendTimeJoustCatalogGoldenRewards, 10800, false, true)
	if err != nil {
		t.Fatal(err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	if shape.baseCount != 2 || shape.activeCount != 2 ||
		len(shape.baseRows) != 2 || len(shape.activeRows) != 2 {
		t.Fatalf("counts base=%d/%d active=%d/%d want 2/2 and 2/2",
			shape.baseCount, len(shape.baseRows), shape.activeCount, len(shape.activeRows))
	}
	if shape.baseRows[0].eventID != uint16(currentSpendTimeEventID) ||
		shape.activeRows[0].eventID != uint16(currentSpendTimeEventID) {
		t.Fatalf("first pair is not 2347: base=%d active=%d",
			shape.baseRows[0].eventID, shape.activeRows[0].eventID)
	}
	if shape.hasEvent(uint16(currentJoustEventID)) {
		t.Fatal("joust-disabled catalog still carries 2365")
	}
	if shape.baseRows[1].eventID != uint16(currentTownNpcVisibleEventID) ||
		shape.activeRows[1].eventID != uint16(currentTownNpcVisibleEventID) {
		t.Fatalf("second pair is not 2395: base=%d active=%d",
			shape.baseRows[1].eventID, shape.activeRows[1].eventID)
	}
	golden := parseCurrentSpendTimeEventInfoBody(t, mustCurrentSpendTimeJoustCatalogGolden(t))
	if !bytes.Equal(shape.baseRows[0].raw, golden.baseRows[0].raw) ||
		!bytes.Equal(shape.activeRows[0].raw, golden.activeRows[0].raw) {
		t.Fatalf("2347 rows changed when only 2395 was enabled: base=%x active=%x",
			shape.baseRows[0].raw, shape.activeRows[0].raw)
	}
}

// TestSendCurrentSpendTimeProtocolStateTownNpcCatalogAddsOnly2395OnTheWire 是**接线**
// 断言（不是纯函数断言）：真实发包路径上，默认档是既有golden加公共181和2037、
// 不含 2395；打开档多出 2395 一对行，而 2347/2365 两行与紧随其后的 op1206 逐字节不变。
func TestSendCurrentSpendTimeProtocolStateTownNpcCatalogAddsOnly2395OnTheWire(t *testing.T) {
	golden := mustCurrentSpendTimeJoustCatalogGolden(t)
	catalog := &currentSpendTimeRuntimeCatalog{
		rewardItemIDs:     currentSpendTimeJoustCatalogGoldenRewards,
		totalStageSeconds: 10800,
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)
	// 2365 跟随马战开关（不受 ambient env 影响），只让本用例的开关决定 2395。
	t.Setenv(currentSpendTimeJoustCatalogEnv, currentSpendTimeJoustCatalogAuto)

	// 默认档：op108 与 golden 加独立安徒恩 fixture 逐字节相同，且不含 2395。
	t.Setenv(currentTownNpcEventCatalogEnv, "")
	offConnection := &bufferConn{}
	offService := &Service{joustCatalog: mustTestJoustEventCatalog(t), gameplayTimers: queue}
	offSession := &gameSession{conn: offConnection, connID: "town-npc-catalog-off"}
	if _, err := offService.sendCurrentSpendTimeProtocolState(offSession, catalog, 7199, 2, true); err != nil {
		t.Fatal(err)
	}
	offDescriptor, offRest := splitGameServerUpperPacketWithHeader(
		t, offConnection.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
	if offDescriptor.Header.Classification != 0 ||
		offDescriptor.Header.MsgID != currentSpendTimeEventInfoMsgID {
		t.Fatalf("off-catalog op108 header=%+v", offDescriptor.Header)
	}
	offBody, err := zlibDecompress(offDescriptor.Body)
	if err != nil {
		t.Fatalf("decompress off-catalog op108: %v", err)
	}
	wantOffBody := withSeriaLuckCatalogFixture(t, withAntonChannelCatalogFixture(t, golden))
	if !bytes.Equal(offBody, wantOffBody) {
		t.Fatalf("off-catalog op108 body=%x want golden plus Anton=%x", offBody, wantOffBody)
	}
	if offShape := parseCurrentSpendTimeEventInfoBody(t, offBody); offShape.hasEvent(uint16(currentTownNpcVisibleEventID)) {
		t.Fatalf("off-catalog op108 still carries event %d", currentTownNpcVisibleEventID)
	}
	// 马战开着时 op108 之后还有 op1240/1241/1242 的既有引导包，这里只钉"紧邻的那一条
	// 仍是 op1206，且两档逐字节相同"。
	offProgress, _ := splitGameServerUpperPacket(t, offRest)
	if offProgress.Header.MsgID != currentSpendTimeProgressMsgID {
		t.Fatalf("off-catalog first trailing packet msg=%d want op1206", offProgress.Header.MsgID)
	}

	// 打开档：只多 2395 的一对行，op1206 逐字节相同。
	t.Setenv(currentTownNpcEventCatalogEnv, "on")
	onConnection := &bufferConn{}
	onService := &Service{joustCatalog: mustTestJoustEventCatalog(t), gameplayTimers: queue}
	onSession := &gameSession{conn: onConnection, connID: "town-npc-catalog-on"}
	if _, err := onService.sendCurrentSpendTimeProtocolState(onSession, catalog, 7199, 2, true); err != nil {
		t.Fatal(err)
	}
	onDescriptor, onRest := splitGameServerUpperPacketWithHeader(
		t, onConnection.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
	onBody, err := zlibDecompress(onDescriptor.Body)
	if err != nil {
		t.Fatalf("decompress on-catalog op108: %v", err)
	}
	onShape := parseCurrentSpendTimeEventInfoBody(t, onBody)
	if onShape.baseCount != 5 || onShape.activeCount != 5 ||
		len(onShape.baseRows) != 5 || len(onShape.activeRows) != 5 {
		t.Fatalf("on-catalog wire counts base=%d/%d active=%d/%d want 5/5 and 5/5",
			onShape.baseCount, len(onShape.baseRows), onShape.activeCount, len(onShape.activeRows))
	}
	if !bytes.Equal(onShape.baseRows[2].raw, mustHexBytes(t, currentTownNpcEventBaseRowHex)) ||
		!bytes.Equal(onShape.activeRows[2].raw, mustHexBytes(t, currentTownNpcEventActiveRowHex)) {
		t.Fatalf("on-catalog 2395 rows base=%x active=%x",
			onShape.baseRows[2].raw, onShape.activeRows[2].raw)
	}
	// 逐字节展开：打开档的 op108 体必须**恰好**等于
	//   u16 3 / golden 的 2347 base 行 / golden 的 2365 base 行 / 2395 base 行 /
	//   u8 3 / golden 的 2347 active 行 / golden 的 2365 active 行 / 2395 active 行
	// 先独立展开 2395，再通过独立 fixture 加入两档都存在的 181；其余字节来自既有 golden。
	wantOnBody := make([]byte, 0, len(golden)+len(mustHexBytes(t, currentTownNpcEventBaseRowHex))+
		len(mustHexBytes(t, currentTownNpcEventActiveRowHex)))
	// golden 里 2347 active 行的起点（146）比 2365 base 行末尾多一个字节：那个字节就是
	// 旧的 active_count(=2)。逐字节展开时必须把它换成新值 3，不能照抄。
	wantOnBody = append(wantOnBody, 3, 0)
	wantOnBody = append(wantOnBody,
		golden[currentSpendTimeJoustCatalogGolden2347BaseStart:currentSpendTimeJoustCatalogGoldenJoustBaseStart]...) // 2347 base
	wantOnBody = append(wantOnBody,
		golden[currentSpendTimeJoustCatalogGoldenJoustBaseStart:currentSpendTimeJoustCatalogGolden2347Active-1]...) // 2365 base
	wantOnBody = append(wantOnBody, mustHexBytes(t, currentTownNpcEventBaseRowHex)...)
	wantOnBody = append(wantOnBody, 3)
	wantOnBody = append(wantOnBody,
		golden[currentSpendTimeJoustCatalogGolden2347Active:currentSpendTimeJoustCatalogGoldenLen]...) // 2347 + 2365 active
	wantOnBody = append(wantOnBody, mustHexBytes(t, currentTownNpcEventActiveRowHex)...)
	wantOnBody = withSeriaLuckCatalogFixture(t, withAntonChannelCatalogFixture(t, wantOnBody))
	if !bytes.Equal(onBody, wantOnBody) {
		t.Fatalf("on-catalog op108 body=%x want %x", onBody, wantOnBody)
	}
	onProgress, _ := splitGameServerUpperPacket(t, onRest)
	if onProgress.Header.MsgID != currentSpendTimeProgressMsgID {
		t.Fatalf("on-catalog first trailing packet msg=%d want op1206", onProgress.Header.MsgID)
	}
	if !bytes.Equal(onProgress.Body, offProgress.Body) {
		t.Fatalf("op1206 differs between catalog modes: on=%x off=%x", onProgress.Body, offProgress.Body)
	}
	t.Logf("wire off-catalog: op108 len=%d sha256=%s; wire on-catalog: op108 len=%d sha256=%s 2347_2365_prefix_equal=true then msg_id=%d",
		len(offBody), currentSpendTimeJoustCatalogDigest(offBody),
		len(onBody), currentSpendTimeJoustCatalogDigest(onBody), onProgress.Header.MsgID)
}

// TestTownNpcEventCatalogEnvOnlyExplicitOnValuesEnable 钉住 env 口径：只有显式打开值
// 才算开，空值/未识别值/off 都保持今天。
func TestTownNpcEventCatalogEnvOnlyExplicitOnValuesEnable(t *testing.T) {
	for _, raw := range []string{"on", "1", "true", "yes", "enable", "enabled", "ON", " On "} {
		t.Setenv(currentTownNpcEventCatalogEnv, raw)
		if !townNpcEventCatalogEnabled() {
			t.Fatalf("%s=%q did not enable the town-NPC event catalog", currentTownNpcEventCatalogEnv, raw)
		}
	}
	for _, raw := range []string{"", "off", "0", "false", "no", "disable", "disabled", "auto", "yes please", "2395"} {
		t.Setenv(currentTownNpcEventCatalogEnv, raw)
		if townNpcEventCatalogEnabled() {
			t.Fatalf("%s=%q unexpectedly enabled the town-NPC event catalog", currentTownNpcEventCatalogEnv, raw)
		}
	}
	// nil Service 必须报 false（与其它实验阀同一 fail-closed 口径）。
	var service *Service
	if service.currentSpendTimeCatalogIncludesTownNpcEvent() {
		t.Fatal("nil service reported the town-NPC event catalog as enabled")
	}
}
