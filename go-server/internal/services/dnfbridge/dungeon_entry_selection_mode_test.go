package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住 op16 `body[7]`（我方 `SelectionMode`）的**第二条独立校验轴**
// `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE`。
//
// 现场缺陷：地轨中心（worldmap area 39）的 310/311/312/313/314 全部用 `body[7]=1`
// 发请求，被我们自己的前置校验误拒成 `failure_reason_byte=19`（= `0x13` =
// `currentSelectDungeonInvalidSelectionStateReason`），整片区域"点了进不去"。
// 依据与厂商逐行对照见 `dungeon_entry_packets.go` 的 `readDungeonEntrySelectionMode` 块。
//
// **默认档（strict）就是改动前行为**：既有用例
// `TestHandleDungeonSelectUpperAnswersUnsupportedEntryMode`（body[7]=1 ⇒ 拒绝）
// 的期望值一个都没动，本文件只新增 `allow` 档的正面证据。

// 线上两条原始 RECV 行（`/opt/dnf90/logs/packet_log.20260927-143657.txt`，只读）里的
// `hex=` 字段，逐字符照抄。RECV 帧 = **13 字节头 + 21 字节体**（日志 `len=34`），
// 所以体在 hex 的第 27 个字符之后。
const (
	// dungeon 313（地轨中心「疾风地带」）⇒ 被拒的那条。
	currentLiveArea39Dungeon313SelectHex = "01100022000000b6493aec9e00390100000000000100ffff00000000000000000000"
	// dungeon 120（同会话里**被接受**的普通请求），用于逐字节对照。
	currentLiveDungeon120SelectHex = "011000220000004d91c4876101780000000200000000ffff00000000000000000000"
	// RECV 帧头长度（`len` − `wire_body_len` = 34 − 21 = 13）。
	currentLiveSelectRecvHeaderBytes = 13
)

func decodeCurrentLiveSelectBody(t *testing.T, wireHex string) []byte {
	t.Helper()
	wire, err := hex.DecodeString(wireHex)
	if err != nil {
		t.Fatalf("decode live wire hex: %v", err)
	}
	if len(wire) != currentLiveSelectRecvHeaderBytes+dungeoncmd.SelectDungeonRequestSize {
		t.Fatalf("live RECV frame len=%d, want %d (13-byte RECV header + 21-byte body)",
			len(wire), currentLiveSelectRecvHeaderBytes+dungeoncmd.SelectDungeonRequestSize)
	}
	return append([]byte(nil), wire[currentLiveSelectRecvHeaderBytes:]...)
}

// TestLiveSelectRequestBodyKeepsSelectionModeAtOffset7 是**字节级定位**断言：
// 把线上两条真实 RECV 体解出来，钉住 `body[7]` 就是 `SelectionMode`，并证明
// "313 进不去 / 120 进得去"的全部线上字节差异只有 `body[4]` 与 `body[7]`。
func TestLiveSelectRequestBodyKeepsSelectionModeAtOffset7(t *testing.T) {
	rejected := decodeCurrentLiveSelectBody(t, currentLiveArea39Dungeon313SelectHex)
	accepted := decodeCurrentLiveSelectBody(t, currentLiveDungeon120SelectHex)

	rejectedRequest, err := dungeoncmd.DecodeSelectDungeonRequest(rejected)
	if err != nil {
		t.Fatal(err)
	}
	if rejectedRequest.DungeonID != 313 ||
		rejectedRequest.Difficulty != 0 ||
		rejectedRequest.EntryOption != 0 ||
		rejectedRequest.SelectionMode != 1 ||
		rejectedRequest.RuntimeState != 0 ||
		rejectedRequest.RuntimeToken != 0xffff ||
		rejectedRequest.Reserved != 0 ||
		rejectedRequest.PartyState != 0 ||
		rejectedRequest.LeaderObjectKey != 0 ||
		rejectedRequest.SpecialMode != 0 ||
		len(rejectedRequest.OpaqueTail) != 0 {
		t.Fatalf("live dungeon 313 body decoded as %+v, want the rejected area-39 request", rejectedRequest)
	}
	// 逐字节：`39 01 00 00 | 00 | 00 00 | 01 | 00 | ff ff | 00 ×10`
	if got := hex.EncodeToString(rejected); got != "390100000000000100ffff00000000000000000000" {
		t.Fatalf("live dungeon 313 body=%s", got)
	}

	acceptedRequest, err := dungeoncmd.DecodeSelectDungeonRequest(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if acceptedRequest.DungeonID != 120 ||
		acceptedRequest.Difficulty != 2 ||
		acceptedRequest.EntryOption != 0 ||
		acceptedRequest.SelectionMode != 0 ||
		acceptedRequest.RuntimeState != 0 ||
		acceptedRequest.RuntimeToken != 0xffff {
		t.Fatalf("live dungeon 120 body decoded as %+v, want the accepted ordinary request", acceptedRequest)
	}
	// 逐字节：`78 00 00 00 | 02 | 00 00 | 00 | 00 | ff ff | 00 ×10`
	if got := hex.EncodeToString(accepted); got != "780000000200000000ffff00000000000000000000" {
		t.Fatalf("live dungeon 120 body=%s", got)
	}

	// 两条体除了各自的副本 id（`body[0:4]`）之外，只允许差 `body[4]`（difficulty）
	// 与 `body[7]`（selection_mode）。
	diffOffsets := make([]int, 0, 2)
	for index := 4; index < len(rejected); index++ {
		if rejected[index] != accepted[index] {
			diffOffsets = append(diffOffsets, index)
		}
	}
	if len(diffOffsets) != 2 || diffOffsets[0] != 4 || diffOffsets[1] != 7 {
		t.Fatalf("live bodies differ at offsets %v (ignoring the dungeon id), want exactly [4 7]", diffOffsets)
	}
}

// TestReadDungeonEntrySelectionModeOnlyAcceptsExplicitAllow 钉住开关语义：
// **只有显式 `allow`** 才放行；其余任何取值（含 `on`/`true`/`1`）都是默认档 strict。
func TestReadDungeonEntrySelectionModeOnlyAcceptsExplicitAllow(t *testing.T) {
	tests := []struct {
		value string
		want  currentDungeonEntrySelectionModeMode
	}{
		{value: "", want: currentDungeonEntrySelectionModeStrict},
		{value: "   ", want: currentDungeonEntrySelectionModeStrict},
		{value: "strict", want: currentDungeonEntrySelectionModeStrict},
		{value: "off", want: currentDungeonEntrySelectionModeStrict},
		{value: "on", want: currentDungeonEntrySelectionModeStrict},
		{value: "true", want: currentDungeonEntrySelectionModeStrict},
		{value: "1", want: currentDungeonEntrySelectionModeStrict},
		{value: "enabled", want: currentDungeonEntrySelectionModeStrict},
		{value: "allow", want: currentDungeonEntrySelectionModeAllow},
		{value: "ALLOW", want: currentDungeonEntrySelectionModeAllow},
		{value: "  Allow  ", want: currentDungeonEntrySelectionModeAllow},
	}
	for _, test := range tests {
		if got := readDungeonEntrySelectionMode(test.value); got != test.want {
			t.Fatalf("readDungeonEntrySelectionMode(%q)=%v want %v", test.value, got, test.want)
		}
	}
	if got := (currentDungeonEntrySelectionModeStrict).String(); got != "strict" {
		t.Fatalf("strict String()=%q", got)
	}
	if got := (currentDungeonEntrySelectionModeAllow).String(); got != dungeonEntrySelectionModeValueAllow {
		t.Fatalf("allow String()=%q", got)
	}
	if (currentDungeonEntrySelectionModeStrict).allowsNonZeroSelectionMode() {
		t.Fatal("strict must not allow a nonzero selection mode")
	}
	if !(currentDungeonEntrySelectionModeAllow).allowsNonZeroSelectionMode() {
		t.Fatal("allow must allow a nonzero selection mode")
	}
}

// TestValidateDungeonEntryRequestSelectionModeAxis 钉住校验轴的开关边界：
// strict 档维持改动前的拒绝，allow 档放行；**其余每一个判据在 allow 档仍然 fail-closed**。
func TestValidateDungeonEntryRequestSelectionModeAxis(t *testing.T) {
	live := decodeCurrentLiveSelectBody(t, currentLiveArea39Dungeon313SelectHex)
	base, err := dungeoncmd.DecodeSelectDungeonRequest(live)
	if err != nil {
		t.Fatal(err)
	}

	// 这条测试钉的是"selection_mode 轴"这条既有契约：把深渊标志的读取位置放回**回退档**
	// （读 body[5:7]，真机 245/245 次恒为 0），body[7] 才仍然只是"selection mode"。
	// 修复档下 body[7] 是厂商 `Flag1 = HellPartyRequestFlag`，由
	// TestLiveOp16BodiesProveTheHellFlagSitsAtBody7 与本函数末尾的新断言覆盖。
	t.Setenv(dungeonHellRequestByteEnv, dungeonHellRequestByteEntryOptionValue)

	// 默认档（strict）：线上 area 39 的请求必须仍被拒（改动前行为逐字节不变）。
	if err := validateCurrentDungeonEntryRequest(base); !errors.Is(err, errDungeonEntryModeUnsupported) {
		t.Fatalf("strict validation error=%v, want errDungeonEntryModeUnsupported", err)
	}
	// 新轴显式 allow：同一条请求放行。
	if err := validateCurrentDungeonEntryRequestForModes(
		base, currentDungeonEntryStrictValidation, currentDungeonEntrySelectionModeAllow); err != nil {
		t.Fatalf("allow validation error=%v, want nil", err)
	}
	// 旧的两参口径（只带 hell 轴 + strict 新轴）不得被新轴改变：它仍然等价于 strict。
	if err := validateCurrentDungeonEntryRequestForModes(
		base, currentDungeonEntryAllowHellOption, currentDungeonEntrySelectionModeStrict); !errors.Is(err, errDungeonEntryModeUnsupported) {
		t.Fatalf("hell-only validation error=%v, want errDungeonEntryModeUnsupported", err)
	}
	// 两条轴互相独立：hell 轴 + 新轴同时放行才都放行。
	if err := validateCurrentDungeonEntryRequestForModes(
		base, currentDungeonEntryAllowHellOption, currentDungeonEntrySelectionModeAllow); err != nil {
		t.Fatalf("both-axes validation error=%v, want nil", err)
	}

	// 修复档（新默认）：同一条线上请求的 body[7] 被正确地当成深渊标志，由深渊那条轴治理，
	// 所以**不再**被这条 selection-mode 规则拒掉（拒掉它才是"开深渊进不去图"的成因）。
	t.Setenv(dungeonHellRequestByteEnv, "")
	if err := validateCurrentDungeonEntryRequest(base); err != nil {
		t.Fatalf("fixed tier must accept the live area-39 request (body[7] is the hell flag), got %v", err)
	}

	// allow 档不是"全放行"：其余判据逐条保持 fail-closed。
	weakened := []struct {
		name   string
		mutate func(*dungeoncmd.SelectDungeonRequest)
	}{
		{name: "special mode", mutate: func(r *dungeoncmd.SelectDungeonRequest) { r.SpecialMode = 1 }},
		{name: "runtime state on an ordinary dungeon", mutate: func(r *dungeoncmd.SelectDungeonRequest) { r.RuntimeState = 1 }},
		{name: "runtime token outside the proved set", mutate: func(r *dungeoncmd.SelectDungeonRequest) { r.RuntimeToken = 0x1234 }},
		{name: "reserved word", mutate: func(r *dungeoncmd.SelectDungeonRequest) { r.Reserved = 1 }},
		{name: "party state", mutate: func(r *dungeoncmd.SelectDungeonRequest) { r.PartyState = 1 }},
		{name: "opaque tail", mutate: func(r *dungeoncmd.SelectDungeonRequest) { r.OpaqueTail = []byte{1} }},
	}
	for _, test := range weakened {
		mutated := base
		test.mutate(&mutated)
		if err := validateCurrentDungeonEntryRequestForModes(
			mutated, currentDungeonEntryStrictValidation, currentDungeonEntrySelectionModeAllow); !errors.Is(err, errDungeonEntryModeUnsupported) {
			t.Fatalf("%s: error=%v, want errDungeonEntryModeUnsupported under allow", test.name, err)
		}
	}
}

// dungeonEntrySelectionModeSelectStream 跑一次完整的 op16 选图请求，返回服务端写出的
// 整条包流。除了 `body[7]`（selection_mode）与开关档位，其余输入完全相同。
func dungeonEntrySelectionModeSelectStream(
	t *testing.T,
	selectionMode byte,
	allow bool,
) (*Service, *gameSession, *bufferConn, []byte) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	if allow {
		service.options.dungeonEntrySelectionMode = currentDungeonEntrySelectionModeAllow
	}
	if got := service.dungeonEntrySelectionModeGate(); got != service.options.dungeonEntrySelectionMode {
		t.Fatalf("selection-mode gate=%v want %v", got, service.options.dungeonEntrySelectionMode)
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                          connection,
		connID:                        "op16-selection-mode-test",
		selectedCharacterID:           99,
		enterSelectDungeonContextSent: true,
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)

	// 与线上同形的 21 字节体：`[0:4]=dungeon`、`[4]=difficulty`、`[7]=selection_mode`、
	// `[9:11]=0xFFFF`，其余全 0。
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)
	body[7] = selectionMode

	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("select dungeon: %v", err)
	}
	return service, session, connection, append([]byte(nil), connection.write.Bytes()...)
}

// TestHandleDungeonSelectUpperHonoursSelectionModeAllow 是 allow 档的正面回归：
// 同一条 `body[7]=1` 请求在 `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE=allow` 下
// 必须走完整条普通进图链（op16 ACK → op5 → op28 → op29），提交 runtime，
// 并且整条流里**没有**任何 `{0,*,0}` 拒绝帧。
func TestHandleDungeonSelectUpperHonoursSelectionModeAllow(t *testing.T) {
	_, session, _, stream := dungeonEntrySelectionModeSelectStream(t, 1, true)
	assertNoDungeonSelectRefusalFrame(t, stream)
	assertEntryChainReachedDungeonInfo(t, stream)
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime == nil || runtime.Dungeon.ID != 700 {
		t.Fatalf("allow path did not commit the runtime: %+v", runtime)
	}
}

// assertEntryChainReachedDungeonInfo 断言 allow 档真的走完了整条普通进图链：
// 流里必须出现 op5（resource state）与 op28（dungeon info）。
func assertEntryChainReachedDungeonInfo(t *testing.T, stream []byte) {
	t.Helper()
	sawResource, sawDungeonInfo := false, false
	rest := stream
	for len(rest) > 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		switch packet.Header.MsgID {
		case currentDungeonResourceStateMsgID:
			sawResource = true
		case currentDungeonInfoNotification:
			sawDungeonInfo = true
		}
		rest = trailing
	}
	if !sawResource || !sawDungeonInfo {
		t.Fatalf("allow path entry chain incomplete: resource_state=%v dungeon_info=%v", sawResource, sawDungeonInfo)
	}
}

// TestSelectionModeAllowKeepsEntryPacketStreamByteIdentical 是等价性证明：
// `body[7]=1` + allow 与 `body[7]=0` + 默认档**写出的整条包流逐字节相同**
// ⇒ 该字节不进任何下行构造器（op5/op27/op28/op29 都不读它），放行它只影响"拒不拒"。
//
// ⚠️ 这条等价性只在**回退档**（深渊标志读 body[5:7]）成立。修复档下 body[7] 就是厂商
// `Flag1 = HellPartyRequestFlag`，它**必须**改变包流（换深渊房图、改 op28/op29）——
// 否则就回到"开深渊也没有柱子"那个 bug。下面两半分别钉这两件事。
func TestSelectionModeAllowKeepsEntryPacketStreamByteIdentical(t *testing.T) {
	t.Setenv(dungeonHellRequestByteEnv, dungeonHellRequestByteEntryOptionValue)
	_, _, _, baseline := dungeonEntrySelectionModeSelectStream(t, 0, false)
	_, _, _, allowed := dungeonEntrySelectionModeSelectStream(t, 1, true)
	if !bytes.Equal(baseline, allowed) {
		t.Fatalf("selection_mode leaked into the entry packet stream:\n baseline=%x\n  allowed=%x",
			baseline, allowed)
	}

	// 修复档（新默认）：body[7]=1 是深渊请求，包流必须**不同**。
	t.Setenv(dungeonHellRequestByteEnv, "")
	_, _, _, fixedPlain := dungeonEntrySelectionModeSelectStream(t, 0, false)
	_, _, _, fixedHell := dungeonEntrySelectionModeSelectStream(t, 1, true)
	if bytes.Equal(fixedPlain, fixedHell) {
		t.Fatal("fixed tier: body[7]=1 (hell party) must change the entry packet stream, but it was byte-identical")
	}
}

// assertNoDungeonSelectRefusalFrame 断言整条流里没有任何 `{0,*,0}` 的 op16 拒绝帧。
func assertNoDungeonSelectRefusalFrame(t *testing.T, stream []byte) {
	t.Helper()
	if len(stream) == 0 {
		t.Fatal("no packets were written at all")
	}
	rest := stream
	packets := 0
	for len(rest) > 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) &&
			len(packet.Body) == 3 && packet.Body[0] == 0 {
			t.Fatalf("allow path emitted an op16 refusal frame: body=%x", packet.Body)
		}
		packets++
		rest = trailing
	}
	if packets < 2 {
		t.Fatalf("allow path wrote only %d packets, want the full entry chain", packets)
	}
}
