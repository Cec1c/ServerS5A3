package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/platform/config"
)

// 本文件是 DNFBRIDGE_JOUST_EVENT_ENABLED 单变量实验的聚焦用例：
//   - 开关默认（以及显式 true）时，马战推送与加开关前逐字节一致；
//   - 开关 false 时 op1240/op1241/op1242/op1243 零下发，op1291 仍有成功 ACK，
//     调度器不注册定时器也不推，op1292 按既有"未受理"结果码安全返回且不落库。

const (
	// joustEventSwitchLegacy 按加开关之前的写法构造 options：完全不提新字段。
	joustEventSwitchLegacy = "legacy"
	// joustEventSwitchEnabled 显式写入"开启"（与 legacy 必须逐字节相同）。
	joustEventSwitchEnabled = "enabled"
	// joustEventSwitchDisabled 显式写入"关闭"。
	joustEventSwitchDisabled = "disabled"
)

type joustEventSwitchFixture struct {
	service      *Service
	session      *gameSession
	connection   *bufferConn
	queue        *fakeCurrentDungeonDeathTimerQueue
	repositories dnfrepo.Group
	round        uint16
}

func joustEventSwitchOptions(mode string) options {
	opts := options{
		accountID:          "account-1",
		gameUpperHeader:    gameUpperHeaderChannel13,
		gameUpperBodyCodec: gameUpperBodyCodecPlain,
	}
	switch mode {
	case joustEventSwitchEnabled:
		opts.joustEventDisabled = false
	case joustEventSwitchDisabled:
		opts.joustEventDisabled = true
	}
	return opts
}

func newJoustEventSwitchFixture(t *testing.T, mode string) joustEventSwitchFixture {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	catalog := mustTestJoustEventCatalog(t)
	const round = uint16(4321)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       90,
		Stats:       map[string]int64{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:7": {ItemID: dnfjoust.PermanentCrystalID, Count: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Unix(7200*int64(round), 0).UTC().Add(10 * time.Minute)
	connection := &bufferConn{}
	service := &Service{
		options:            joustEventSwitchOptions(mode),
		pvfItemCatalog:     mustTestJoustCatalog(t),
		joustCatalog:       catalog,
		gameplayTimers:     queue,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "joust-event-switch-" + mode, selectedCharacterID: 19}
	service.gameSessions = map[uint16]*gameSession{19: session}
	return joustEventSwitchFixture{
		service:      service,
		session:      session,
		connection:   connection,
		queue:        queue,
		repositories: repositories,
		round:        round,
	}
}

// openingWire 走一遍登录/op108 开场推送、面板刷新、四分之一决赛边界推送，
// 以及下注阶段的调度器边界推送，返回这几条路径写在连接上的原始字节。
func (fixture joustEventSwitchFixture) openingWire(t *testing.T) []byte {
	t.Helper()
	if err := fixture.service.sendCurrentJoustOpeningState(fixture.session); err != nil {
		t.Fatalf("opening state: %v", err)
	}
	if err := fixture.service.sendCurrentJoustOpeningRoster(fixture.session); err != nil {
		t.Fatalf("opening roster: %v", err)
	}
	if err := fixture.service.pushCurrentJoustBoundarySnapshot(context.Background(), fixture.session, dnfjoust.Timeline{
		Round: fixture.round,
		Phase: dnfjoust.PhaseQuarterFinal,
		State: 2,
		Stage: 0,
	}); err != nil {
		t.Fatalf("quarter-final boundary: %v", err)
	}
	fixture.service.handleCurrentJoustBoundary(fixture.queue.Now())
	return append([]byte(nil), fixture.connection.write.Bytes()...)
}

func splitAllGameServerUpperPackets(t *testing.T, data []byte) []dnfproto.ChannelPacket {
	t.Helper()
	packets := make([]dnfproto.ChannelPacket, 0, 8)
	rest := data
	for len(rest) != 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		if len(trailing) >= len(rest) {
			t.Fatalf("joust switch wire stalled at %x", rest)
		}
		packets = append(packets, packet)
		rest = trailing
	}
	return packets
}

func TestJoustEventOptionReadsEnvironmentSwitchAndDefaultsToEnabled(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "true", want: true},
		{value: "1", want: true},
		{value: "yes", want: true},
		{value: "on", want: true},
		{value: "false", want: false},
		{value: "0", want: false},
		{value: "no", want: false},
		{value: "off", want: false},
	} {
		t.Run("DNFBRIDGE_JOUST_EVENT_ENABLED="+test.value, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_JOUST_EVENT_ENABLED", test.value)
			service := &Service{options: optionsFromConfig(config.ServiceConfig{})}
			if got := service.joustEventEnabled(); got != test.want {
				t.Fatalf("joust event enabled=%t want=%t", got, test.want)
			}
		})
	}
	// 零值 options 是加开关之前的构造方式（测试与探针里的 Service 字面量），
	// 它必须保持事件开启，否则"默认 true 行为不变"就不成立。
	if !(&Service{}).joustEventEnabled() {
		t.Fatal("zero-value options disabled the joust event")
	}
	if (*Service)(nil).joustEventEnabled() {
		t.Fatal("nil service reported the joust event as enabled")
	}
	if (&Service{options: options{joustEventDisabled: true}}).joustEventEnabled() {
		t.Fatal("explicitly disabled joust event reported as enabled")
	}
}

func TestCurrentJoustEventEnabledWireMatchesLegacyAndDisabledWireIsEmpty(t *testing.T) {
	legacy := newJoustEventSwitchFixture(t, joustEventSwitchLegacy)
	enabled := newJoustEventSwitchFixture(t, joustEventSwitchEnabled)
	disabled := newJoustEventSwitchFixture(t, joustEventSwitchDisabled)
	if !legacy.service.joustEventEnabled() || !enabled.service.joustEventEnabled() {
		t.Fatal("default or explicitly enabled joust event is not active")
	}
	if disabled.service.joustEventEnabled() {
		t.Fatal("disabled joust event is active")
	}

	legacyWire := legacy.openingWire(t)
	enabledWire := enabled.openingWire(t)
	if !bytes.Equal(legacyWire, enabledWire) {
		t.Fatalf("enabled wire=%x legacy wire=%x", enabledWire, legacyWire)
	}
	if len(legacyWire) == 0 {
		t.Fatal("enabled joust event wrote no packets")
	}
	wantMsgIDs := []uint16{
		currentJoustStatePushMsgID,
		currentJoustRosterPushMsgID,
		currentJoustPoolPushMsgID,
		currentJoustStatePushMsgID,
		currentJoustMatchPushMsgID,
		currentJoustStatePushMsgID,
		currentJoustRosterPushMsgID,
		currentJoustPoolPushMsgID,
	}
	wantBodyLens := []int{3, 90, 46, 3, 31, 3, 90, 46}
	packets := splitAllGameServerUpperPackets(t, legacyWire)
	if len(packets) != len(wantMsgIDs) {
		t.Fatalf("enabled joust packets=%d want=%d wire=%x", len(packets), len(wantMsgIDs), legacyWire)
	}
	for index, packet := range packets {
		if packet.Header.Classification != 0 || packet.Header.MsgID != wantMsgIDs[index] || len(packet.Body) != wantBodyLens[index] {
			t.Fatalf("joust packet[%d] header=%+v body_len=%d want_msg=%d want_len=%d",
				index, packet.Header, len(packet.Body), wantMsgIDs[index], wantBodyLens[index])
		}
	}
	if got := binary.LittleEndian.Uint16(packets[0].Body[:2]); got != legacy.round {
		t.Fatalf("opening state round=%d want=%d", got, legacy.round)
	}
	if packets[0].Body[2] != currentJoustStateOpening || packets[3].Body[2] != 2 {
		t.Fatalf("opening state=%d quarter-final state=%d", packets[0].Body[2], packets[3].Body[2])
	}

	if disabledWire := disabled.openingWire(t); len(disabledWire) != 0 {
		t.Fatalf("disabled joust event wrote %d bytes=%x", len(disabledWire), disabledWire)
	}
}

func TestCurrentJoustEventDisabledKeepsOp1291AckAndSkipsPanelRefresh(t *testing.T) {
	disabled := newJoustEventSwitchFixture(t, joustEventSwitchDisabled)
	if err := handleCurrentJoustInfo(disabled.service, disabled.session, nil); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, disabled.connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketJoustInfo) ||
		len(ack.Body) != 5 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:]) != 0 {
		t.Fatalf("disabled op1291 ack header=%+v body=%x", ack.Header, ack.Body)
	}
	if len(trailing) != 0 {
		t.Fatalf("disabled op1291 still refreshed the panel trailing=%x", trailing)
	}

	// 同一份夹具在默认（未设置开关）时仍然是"ACK + op1241 + op1242"：
	// 被开关扣掉的只有面板刷新。
	legacy := newJoustEventSwitchFixture(t, joustEventSwitchLegacy)
	if err := handleCurrentJoustInfo(legacy.service, legacy.session, nil); err != nil {
		t.Fatal(err)
	}
	packets := splitAllGameServerUpperPackets(t, legacy.connection.write.Bytes())
	if len(packets) != 3 ||
		packets[0].Header.MsgID != uint16(dnfenum.CmdPacketJoustInfo) ||
		packets[1].Header.MsgID != currentJoustRosterPushMsgID ||
		packets[2].Header.MsgID != currentJoustPoolPushMsgID {
		t.Fatalf("default op1291 packets=%+v", packets)
	}
}

func TestCurrentJoustSchedulerRegistersNothingWhenEventDisabled(t *testing.T) {
	disabled := newJoustEventSwitchFixture(t, joustEventSwitchDisabled)
	disabled.service.startCurrentJoustScheduler(context.Background())
	if disabled.queue.scheduleCount != 0 || disabled.connection.write.Len() != 0 {
		t.Fatalf("disabled scheduler timers=%d wire=%x", disabled.queue.scheduleCount, disabled.connection.write.Bytes())
	}

	legacy := newJoustEventSwitchFixture(t, joustEventSwitchLegacy)
	legacy.service.startCurrentJoustScheduler(context.Background())
	if legacy.queue.scheduleCount != 1 {
		t.Fatalf("default scheduler timers=%d want=1", legacy.queue.scheduleCount)
	}
}

func TestCurrentJoustEventDisabledRejectsBettingWithoutCommitAndLogsSwitch(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	disabled := newJoustEventSwitchFixture(t, joustEventSwitchDisabled)
	disabled.service.packetLog = logger
	// 与 joust_betting_test.go 里被证实的 captured 业务体一致：
	// {u8 knight=1,u32 slot=7,u32 amount=3}。
	body := []byte{1, 7, 0, 0, 0, 3, 0, 0, 0}
	if err := disabled.service.handleCurrentJoustBetting(disabled.session, body); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, disabled.connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketJoustBetting) || len(ack.Body) != 5 ||
		ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:]) != currentJoustBettingResultInvalid {
		t.Fatalf("disabled op1292 ack header=%+v body=%x", ack.Header, ack.Body)
	}
	if len(trailing) != 0 {
		t.Fatalf("disabled op1292 wrote extra packets trailing=%x", trailing)
	}
	character, found, err := disabled.repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if character.Stats[dnfjoust.RoundStat] != 0 || character.Stats[dnfjoust.PendingStat] != 0 {
		t.Fatalf("disabled op1292 wrote the ledger=%v", character.Stats)
	}
	inventory, found, err := disabled.repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:7"].Count != 10 {
		t.Fatalf("disabled op1292 debited the source slot=%+v", inventory.Slots["0:7"])
	}
	for slot, stack := range inventory.Slots {
		if stack.ItemID == dnfjoust.ParticipationItemID {
			t.Fatalf("disabled op1292 granted the participation reward slot=%s stack=%+v", slot, stack)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	if !strings.Contains(text, "kind=game-joust-event-disabled") ||
		!strings.Contains(text, "source=op1292_betting") ||
		!strings.Contains(text, "reason=joust_event_disabled") {
		t.Fatalf("disabled op1292 log=%q", text)
	}

	// 默认（未设置开关）下同一份请求正常落库：差异只来自开关这一个变量。
	legacy := newJoustEventSwitchFixture(t, joustEventSwitchLegacy)
	if err := legacy.service.handleCurrentJoustBetting(legacy.session, body); err != nil {
		t.Fatal(err)
	}
	inventory, found, err = legacy.repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load default inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:7"].Count != 7 {
		t.Fatalf("default op1292 source slot=%+v want=7", inventory.Slots["0:7"])
	}
}
