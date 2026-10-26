package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/platform/config"
)

// 本文件是"op108 在线活动目录里 2365（竞技大赛/马战）的 base+active 两行必须跟随
// 马战开关"的聚焦用例。
//
// 现场症状：登录进城镇后客户端自动弹出韩文 BLUE/RED 竞技大赛窗。op1240/op1241/
// op1242/op1243 四条推送已被 DNFBRIDGE_JOUST_EVENT_ENABLED=0 抑制，但 op108 目录里
// 2365 的两行此前无条件下发，客户端能只凭这个目录把该活动判成全天开放。
//
// 三条判据：
//  1. 开关 on（默认/legacy）⇒ op108 与 2347 可见入口修复后的当前基准逐字节相同（golden hex）；
//  2. 开关 off          ⇒ 目录里没有 2365，且 base_catalog_count/active_count 与
//     实际行数自洽（逐字段解析回读，不是 grep 字节）；2347 两行逐字节不变；
//  3. 新 env DNFBRIDGE_JOUST_EVENT_CATALOG 的 off/on 两档覆盖上面两种行为。

// currentSpendTimeJoustCatalogGoldenHex 是 2347 可见入口与马战默认开启时的当前固定报文
// buildCurrentSpendTimeEventInfoBody(
//
//	[]uint32{490006574, 490006575, 490006576, 490006577}, 10800)
//
// 的 246 字节原文。它同时是 on 档的 golden，也是 off 档里 2347 两行的比对基准。
const currentSpendTimeJoustCatalogGoldenHex = "02002b090100000000000000000000000100040c000000c0dbbcc6d4dacfdfbdb1c0f80000000032000000c0dbbcc6d4dacfdfcab1bce4b4efb5bdb6d4d3a6bdd7b6ced2aac7f3baf3a3acbfc9c1ecc8a1b8c3bdd7b6cebdb1c0f8a1a30000000000000000140000007370656e642074696d65206576656e742f302f3000000000013d0901000000000000000000000000022b092ee8341d2fe8341d30e8341d31e8341dffffffffffffffffffffffffffffffffffffffffffffffff04000000302a00003d09000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// golden body 里各段的绝对偏移（与 online_event_spend_time_protocol.go 的写序一一
// 对应：u16 base_catalog_count / 2347 base row / 2365 base row / u8 active_count /
// 2347 active row / 2365 active row）。
const (
	currentSpendTimeJoustCatalogGoldenLen            = 246
	currentSpendTimeJoustCatalogOffLen               = 181
	currentSpendTimeJoustCatalogGolden2347BaseStart  = 2
	currentSpendTimeJoustCatalogGoldenJoustBaseStart = 130
	currentSpendTimeJoustCatalogGolden2347Active     = 146
	currentSpendTimeJoustCatalogGoldenJoustActive    = 196
)

var currentSpendTimeJoustCatalogGoldenRewards = []uint32{490006574, 490006575, 490006576, 490006577}

func mustCurrentSpendTimeJoustCatalogGolden(t *testing.T) []byte {
	t.Helper()
	body, err := hex.DecodeString(currentSpendTimeJoustCatalogGoldenHex)
	if err != nil {
		t.Fatalf("decode golden op108 body: %v", err)
	}
	if len(body) != currentSpendTimeJoustCatalogGoldenLen {
		t.Fatalf("golden op108 body len=%d want=%d", len(body), currentSpendTimeJoustCatalogGoldenLen)
	}
	return body
}

// currentSpendTimeJoustCatalogDigest 只用于把"逐字节相同/不同"的结论写进 -v 输出，
// 便于现场把测试日志与 packet_log 里的 op108 直接对上。
func currentSpendTimeJoustCatalogDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// currentSpendTimeCatalogReader 按 op108 目录的真实语法逐字段前进；任何越界都会
// 立刻失败，因此"计数与行数自洽"是被解析器验证的，而不是被 grep 出来的。
type currentSpendTimeCatalogReader struct {
	body   []byte
	offset int
}

func (reader *currentSpendTimeCatalogReader) remaining() int {
	return len(reader.body) - reader.offset
}

func (reader *currentSpendTimeCatalogReader) take(t *testing.T, count int) []byte {
	t.Helper()
	if count < 0 || reader.remaining() < count {
		t.Fatalf("op108 body truncated: want %d bytes at offset %d of %d",
			count, reader.offset, len(reader.body))
	}
	data := reader.body[reader.offset : reader.offset+count]
	reader.offset += count
	return data
}

func (reader *currentSpendTimeCatalogReader) byteValue(t *testing.T) byte {
	t.Helper()
	return reader.take(t, 1)[0]
}

func (reader *currentSpendTimeCatalogReader) uint16Value(t *testing.T) uint16 {
	t.Helper()
	return binary.LittleEndian.Uint16(reader.take(t, 2))
}

func (reader *currentSpendTimeCatalogReader) uint32Value(t *testing.T) uint32 {
	t.Helper()
	return binary.LittleEndian.Uint32(reader.take(t, 4))
}

func (reader *currentSpendTimeCatalogReader) dstr(t *testing.T) []byte {
	t.Helper()
	return reader.take(t, int(reader.uint32Value(t)))
}

type currentSpendTimeCatalogBaseRow struct {
	eventID     uint16
	value       uint32
	hasExtended bool
	route       string
	start       uint32
	end         uint32
	raw         []byte
}

type currentSpendTimeCatalogActiveRow struct {
	eventID uint16
	params  [currentSpendTimeDescriptorParamCount]uint32
	raw     []byte
}

type currentSpendTimeCatalogShape struct {
	baseCount   uint16
	baseRows    []currentSpendTimeCatalogBaseRow
	activeCount byte
	activeRows  []currentSpendTimeCatalogActiveRow
}

func (shape currentSpendTimeCatalogShape) hasEvent(eventID uint16) bool {
	for _, row := range shape.baseRows {
		if row.eventID == eventID {
			return true
		}
	}
	for _, row := range shape.activeRows {
		if row.eventID == eventID {
			return true
		}
	}
	return false
}

func parseCurrentSpendTimeEventInfoBody(t *testing.T, body []byte) currentSpendTimeCatalogShape {
	t.Helper()
	// Test-reader bound: public 13 rows plus FiendWar and five cumulative town
	// activities. This is not a server or native receive admission limit.
	const maxCatalogRows = 19
	reader := &currentSpendTimeCatalogReader{body: body}
	shape := currentSpendTimeCatalogShape{baseCount: reader.uint16Value(t)}
	if shape.baseCount == 0 || int(shape.baseCount) > maxCatalogRows {
		t.Fatalf("op108 base_catalog_count=%d is outside 1..%d",
			shape.baseCount, maxCatalogRows)
	}
	for index := 0; index < int(shape.baseCount); index++ {
		start := reader.offset
		var row currentSpendTimeCatalogBaseRow
		row.eventID = reader.uint16Value(t)
		row.value = reader.uint32Value(t)
		reader.dstr(t) // generic title
		reader.dstr(t) // generic description
		row.hasExtended = reader.byteValue(t) == 1
		if row.hasExtended {
			reader.byteValue(t) // generic category flags
			reader.byteValue(t) // generic presentation flags
			reader.dstr(t)      // optional title override
			reader.dstr(t)      // optional subtitle override
			reader.dstr(t)      // optional description override
			row.start = reader.uint32Value(t)
			row.end = reader.uint32Value(t)
			row.route = string(reader.dstr(t))
			reader.dstr(t) // optional status text
			reader.byteValue(t)
		}
		row.raw = body[start:reader.offset]
		shape.baseRows = append(shape.baseRows, row)
	}
	shape.activeCount = reader.byteValue(t)
	if shape.activeCount == 0 || int(shape.activeCount) > maxCatalogRows {
		t.Fatalf("op108 active_count=%d is outside 1..%d",
			shape.activeCount, maxCatalogRows)
	}
	for index := 0; index < int(shape.activeCount); index++ {
		var row currentSpendTimeCatalogActiveRow
		start := reader.offset
		row.eventID = reader.uint16Value(t)
		for param := 0; param < currentSpendTimeDescriptorParamCount; param++ {
			row.params[param] = reader.uint32Value(t)
		}
		row.raw = body[start:reader.offset]
		shape.activeRows = append(shape.activeRows, row)
	}
	if reader.remaining() != 0 {
		t.Fatalf("op108 body has %d trailing bytes after %d base rows and %d active rows",
			reader.remaining(), len(shape.baseRows), len(shape.activeRows))
	}
	return shape
}

// TestCurrentSpendTimeEventInfoCatalogWithJoustKeepsTheGoldenBody 钉住 on 档：
// 只要 2365 还在，op108 就必须与改动前逐字节相同（将来重新启用马战时不引入回归）。
func TestCurrentSpendTimeEventInfoCatalogWithJoustKeepsTheGoldenBody(t *testing.T) {
	golden := mustCurrentSpendTimeJoustCatalogGolden(t)
	body, err := buildCurrentSpendTimeEventInfoBody(currentSpendTimeJoustCatalogGoldenRewards, 10800, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, golden) {
		t.Fatalf("on-catalog op108 body=%x want golden=%x", body, golden)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	if shape.baseCount != 2 || shape.activeCount != 2 ||
		len(shape.baseRows) != 2 || len(shape.activeRows) != 2 {
		t.Fatalf("on-catalog counts base=%d/%d active=%d/%d",
			shape.baseCount, len(shape.baseRows), shape.activeCount, len(shape.activeRows))
	}
	twentyThree := shape.baseRows[0]
	if twentyThree.eventID != uint16(currentSpendTimeEventID) || !twentyThree.hasExtended ||
		twentyThree.route != currentSpendTimeEventRoute {
		t.Fatalf("2347 base row=%+v", twentyThree)
	}
	joust := shape.baseRows[1]
	if joust.eventID != uint16(currentJoustEventID) || joust.hasExtended {
		t.Fatalf("2365 base row=%+v", joust)
	}
	if shape.activeRows[1].eventID != uint16(currentJoustEventID) {
		t.Fatalf("2365 active row=%+v", shape.activeRows[1])
	}
	t.Logf("op108 on-catalog byte-identical to golden: len=%d sha256=%s base_catalog_count=%d active_count=%d",
		len(body), currentSpendTimeJoustCatalogDigest(body), shape.baseCount, shape.activeCount)
}

// TestCurrentSpendTimeEventInfoCatalogWithoutJoustKeepsCountsConsistent 是本次修复的
// 核心判据：off 档下 2365 两行不出现，两个计数与真实行数一致，2347 两行逐字节不变。
func TestCurrentSpendTimeEventInfoCatalogWithoutJoustKeepsCountsConsistent(t *testing.T) {
	golden := mustCurrentSpendTimeJoustCatalogGolden(t)
	body, err := buildCurrentSpendTimeEventInfoBody(currentSpendTimeJoustCatalogGoldenRewards, 10800, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != currentSpendTimeJoustCatalogOffLen {
		t.Fatalf("off-catalog op108 body len=%d want=%d body=%x",
			len(body), currentSpendTimeJoustCatalogOffLen, body)
	}

	// 逐字段解析回读：计数必须等于真实行数，且解析器必须正好消费完整 body。
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	if shape.baseCount != 1 || len(shape.baseRows) != 1 {
		t.Fatalf("off-catalog base_catalog_count=%d parsed_rows=%d", shape.baseCount, len(shape.baseRows))
	}
	if shape.activeCount != 1 || len(shape.activeRows) != 1 {
		t.Fatalf("off-catalog active_count=%d parsed_rows=%d", shape.activeCount, len(shape.activeRows))
	}
	if shape.hasEvent(uint16(currentJoustEventID)) {
		t.Fatalf("off-catalog still carries 2365: base=%+v active=%+v", shape.baseRows, shape.activeRows)
	}

	// 2347 两行逐字节等于 golden 里的同一段。
	goldenBase := golden[currentSpendTimeJoustCatalogGolden2347BaseStart:currentSpendTimeJoustCatalogGoldenJoustBaseStart]
	goldenActive := golden[currentSpendTimeJoustCatalogGolden2347Active:currentSpendTimeJoustCatalogGoldenJoustActive]
	if !bytes.Equal(shape.baseRows[0].raw, goldenBase) {
		t.Fatalf("2347 base row=%x want=%x", shape.baseRows[0].raw, goldenBase)
	}
	if !bytes.Equal(shape.activeRows[0].raw, goldenActive) {
		t.Fatalf("2347 active row=%x want=%x", shape.activeRows[0].raw, goldenActive)
	}
	if shape.baseRows[0].eventID != uint16(currentSpendTimeEventID) ||
		!shape.baseRows[0].hasExtended ||
		shape.baseRows[0].route != currentSpendTimeEventRoute {
		t.Fatalf("off-catalog 2347 base row=%+v", shape.baseRows[0])
	}
	if shape.activeRows[0].eventID != uint16(currentSpendTimeEventID) {
		t.Fatalf("off-catalog 2347 active row=%+v", shape.activeRows[0])
	}

	// 整条 body 只能是"计数 1/1 + golden 里 2347 的两段"：这条断言同时证明
	// 2365 的 65 个字节（base 15 + active 50）一个都没留下。
	wantOff := make([]byte, 0, currentSpendTimeJoustCatalogOffLen)
	wantOff = append(wantOff, 1, 0) // base_catalog_count = 1
	wantOff = append(wantOff, goldenBase...)
	wantOff = append(wantOff, 1) // active_count = 1
	wantOff = append(wantOff, goldenActive...)
	if !bytes.Equal(body, wantOff) {
		t.Fatalf("off-catalog op108 body=%x want=%x", body, wantOff)
	}

	// 2365 的字面量（u16 LE 0x093d）不该出现在任何偏移上。
	joustEventIDBytes := []byte{byte(currentJoustEventID & 0xff), byte((currentJoustEventID >> 8) & 0xff)}
	if index := bytes.Index(body, joustEventIDBytes); index >= 0 {
		t.Fatalf("off-catalog op108 body still contains the 2365 event id at offset %d: %x", index, body)
	}
	t.Logf("op108 off-catalog: len=%d sha256=%s base_catalog_count=%d base_rows=%d active_count=%d active_rows=%d 2365_present=%t 2347_base_sha256=%s 2347_active_sha256=%s",
		len(body), currentSpendTimeJoustCatalogDigest(body),
		shape.baseCount, len(shape.baseRows), shape.activeCount, len(shape.activeRows),
		shape.hasEvent(uint16(currentJoustEventID)),
		currentSpendTimeJoustCatalogDigest(shape.baseRows[0].raw),
		currentSpendTimeJoustCatalogDigest(shape.activeRows[0].raw))
}

// TestCurrentSpendTimeJoustCatalogSwitchFollowsJoustEventSwitchAndNewEnv 覆盖三档：
// 默认跟随 DNFBRIDGE_JOUST_EVENT_ENABLED、新 env 显式 off/on 覆盖、非法值回退 auto。
func TestCurrentSpendTimeJoustCatalogSwitchFollowsJoustEventSwitchAndNewEnv(t *testing.T) {
	tests := []struct {
		name         string
		joustEnabled string
		catalogRaw   string
		wantMode     string
		wantInclude  bool
		wantBad      bool
	}{
		{name: "unset follows an enabled joust event", wantMode: currentSpendTimeJoustCatalogAuto, wantInclude: true},
		{name: "joust disabled drops 2365", joustEnabled: "0",
			wantMode: currentSpendTimeJoustCatalogAuto, wantInclude: false},
		{name: "joust disabled plus catalog auto drops 2365", joustEnabled: "off", catalogRaw: "auto",
			wantMode: currentSpendTimeJoustCatalogAuto, wantInclude: false},
		{name: "catalog off alone drops 2365", catalogRaw: "off",
			wantMode: currentSpendTimeJoustCatalogOff, wantInclude: false},
		{name: "catalog off overrides an enabled joust event", joustEnabled: "1", catalogRaw: "off",
			wantMode: currentSpendTimeJoustCatalogOff, wantInclude: false},
		{name: "catalog on alone keeps 2365", catalogRaw: "on",
			wantMode: currentSpendTimeJoustCatalogOn, wantInclude: true},
		{name: "catalog on forces 2365 while joust is disabled", joustEnabled: "0", catalogRaw: "on",
			wantMode: currentSpendTimeJoustCatalogOn, wantInclude: true},
		{name: "invalid catalog value falls back to auto", joustEnabled: "0", catalogRaw: "yes-please",
			wantMode: currentSpendTimeJoustCatalogAuto, wantInclude: false, wantBad: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_JOUST_EVENT_ENABLED", test.joustEnabled)
			t.Setenv(currentSpendTimeJoustCatalogEnv, test.catalogRaw)
			service := &Service{options: optionsFromConfig(config.ServiceConfig{})}
			if got := service.currentSpendTimeJoustCatalogMode(); got != test.wantMode {
				t.Fatalf("catalog mode=%q want=%q (env=%q)", got, test.wantMode, test.catalogRaw)
			}
			if got := service.currentSpendTimeCatalogIncludesJoustEvent(); got != test.wantInclude {
				t.Fatalf("includes 2365=%t want=%t (joust=%q catalog=%q)",
					got, test.wantInclude, test.joustEnabled, test.catalogRaw)
			}
			if got := service.options.joustEventCatalogEnvBad; got != test.wantBad {
				t.Fatalf("catalog env bad=%t want=%t (env=%q)", got, test.wantBad, test.catalogRaw)
			}
		})
	}

	// 零值 options 是加开关之前的构造方式（测试与探针里的 Service 字面量），
	// 它必须保持"目录里带 2365"，否则"默认行为不变"就不成立。
	if !(&Service{}).currentSpendTimeCatalogIncludesJoustEvent() {
		t.Fatal("zero-value options dropped 2365 from the op108 catalog")
	}
	if (*Service)(nil).currentSpendTimeCatalogIncludesJoustEvent() {
		t.Fatal("nil service reported 2365 as catalogued")
	}
	if (&Service{options: options{joustEventDisabled: true}}).currentSpendTimeCatalogIncludesJoustEvent() {
		t.Fatal("disabled joust event still catalogued 2365")
	}
	if !(&Service{options: options{
		joustEventDisabled:    true,
		joustEventCatalogMode: currentSpendTimeJoustCatalogOn,
	}}).currentSpendTimeCatalogIncludesJoustEvent() {
		t.Fatal("explicit catalog=on did not force 2365 back")
	}

	for _, test := range []struct {
		raw  string
		mode string
		bad  bool
	}{
		{raw: "", mode: currentSpendTimeJoustCatalogAuto},
		{raw: " AUTO ", mode: currentSpendTimeJoustCatalogAuto},
		{raw: "follow", mode: currentSpendTimeJoustCatalogAuto},
		{raw: "OFF", mode: currentSpendTimeJoustCatalogOff},
		{raw: "0", mode: currentSpendTimeJoustCatalogOff},
		{raw: "false", mode: currentSpendTimeJoustCatalogOff},
		{raw: "on", mode: currentSpendTimeJoustCatalogOn},
		{raw: "yes", mode: currentSpendTimeJoustCatalogOn},
		{raw: "true", mode: currentSpendTimeJoustCatalogOn},
		{raw: "maybe", mode: currentSpendTimeJoustCatalogAuto, bad: true},
	} {
		mode, bad := normalizeCurrentSpendTimeJoustCatalogMode(test.raw)
		if mode != test.mode || bad != test.bad {
			t.Fatalf("normalize(%q)=%q/bad=%t want=%q/bad=%t", test.raw, mode, bad, test.mode, test.bad)
		}
	}
}

// TestSendCurrentSpendTimeProtocolStateJoustCatalogOffDrops2365FromOp108Only 在真实
// 发包路径上对照两档：off 档保留2347、181和2037，op1206与on档相同；
// on档的原有行与golden相同，公共181/2037行由独立夹具追加。
func TestSendCurrentSpendTimeProtocolStateJoustCatalogOffDrops2365FromOp108Only(t *testing.T) {
	golden := mustCurrentSpendTimeJoustCatalogGolden(t)
	catalog := &currentSpendTimeRuntimeCatalog{
		rewardItemIDs:     currentSpendTimeJoustCatalogGoldenRewards,
		totalStageSeconds: 10800,
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)

	offConnection := &bufferConn{}
	offService := &Service{options: options{joustEventDisabled: true}, gameplayTimers: queue}
	offSession := &gameSession{conn: offConnection, connID: "joust-catalog-off"}
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
	offShape := parseCurrentSpendTimeEventInfoBody(t, offBody)
	if offShape.baseCount != 3 || offShape.activeCount != 3 ||
		len(offShape.baseRows) != 3 || len(offShape.activeRows) != 3 ||
		!offShape.hasEvent(181) ||
		!offShape.hasEvent(currentSeriaLuckEventID) ||
		offShape.hasEvent(uint16(currentJoustEventID)) {
		t.Fatalf("off-catalog wire counts base=%d/%d active=%d/%d rows=%+v/%+v",
			offShape.baseCount, len(offShape.baseRows),
			offShape.activeCount, len(offShape.activeRows),
			offShape.baseRows, offShape.activeRows)
	}
	if !bytes.Equal(offShape.baseRows[0].raw,
		golden[currentSpendTimeJoustCatalogGolden2347BaseStart:currentSpendTimeJoustCatalogGoldenJoustBaseStart]) ||
		!bytes.Equal(offShape.activeRows[0].raw,
			golden[currentSpendTimeJoustCatalogGolden2347Active:currentSpendTimeJoustCatalogGoldenJoustActive]) {
		t.Fatalf("off-catalog 2347 rows changed: base=%x active=%x",
			offShape.baseRows[0].raw, offShape.activeRows[0].raw)
	}

	// 事件关着时 op1240/op1241/op1242 本来就不发，op108 之后只能是 op1206。
	offProgress, offTrailing := splitGameServerUpperPacket(t, offRest)
	if offProgress.Header.MsgID != currentSpendTimeProgressMsgID || len(offTrailing) != 0 {
		t.Fatalf("off-catalog trailing packets msg=%d trailing=%x",
			offProgress.Header.MsgID, offTrailing)
	}

	onConnection := &bufferConn{}
	onService := &Service{joustCatalog: mustTestJoustEventCatalog(t), gameplayTimers: queue}
	onSession := &gameSession{conn: onConnection, connID: "joust-catalog-on"}
	if _, err := onService.sendCurrentSpendTimeProtocolState(onSession, catalog, 7199, 2, true); err != nil {
		t.Fatal(err)
	}
	onDescriptor, onRest := splitGameServerUpperPacketWithHeader(
		t, onConnection.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
	onBody, err := zlibDecompress(onDescriptor.Body)
	if err != nil {
		t.Fatalf("decompress on-catalog op108: %v", err)
	}
	wantOn := withSeriaLuckCatalogFixture(t, withAntonChannelCatalogFixture(t, golden))
	if !bytes.Equal(onBody, wantOn) {
		t.Fatalf("on-catalog op108 body=%x want golden plus Anton=%x", onBody, wantOn)
	}
	onProgress, _ := splitGameServerUpperPacket(t, onRest)
	if !bytes.Equal(onProgress.Body, offProgress.Body) {
		t.Fatalf("op1206 differs between catalog modes: on=%x off=%x", onProgress.Body, offProgress.Body)
	}

	// env 兜底在真实服务对象上也生效：JOUST=0 + CATALOG=on 时目录里必须仍有 2365。
	t.Setenv("DNFBRIDGE_JOUST_EVENT_ENABLED", "0")
	t.Setenv(currentSpendTimeJoustCatalogEnv, "on")
	forced := &Service{options: optionsFromConfig(config.ServiceConfig{})}
	if !forced.currentSpendTimeCatalogIncludesJoustEvent() {
		t.Fatal("DNFBRIDGE_JOUST_EVENT_CATALOG=on did not force 2365 back into the catalog")
	}
	if mode := forced.currentSpendTimeJoustCatalogMode(); mode != currentSpendTimeJoustCatalogOn {
		t.Fatalf("forced catalog mode=%q", mode)
	}
	t.Logf("wire off-catalog: op108 len=%d sha256=%s then msg_id=%d (no trailing); wire on-catalog: op108 len=%d sha256=%s legacy_plus_anton_equal=true",
		len(offBody), currentSpendTimeJoustCatalogDigest(offBody), offProgress.Header.MsgID,
		len(onBody), currentSpendTimeJoustCatalogDigest(onBody))
}
