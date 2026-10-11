package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/platform/config"
)

// 本文件是 DNFBRIDGE_STORY_DIGEST_ENABLED 单变量实验的聚焦用例：
//   - 开关默认（以及显式 true）时，选角 ack 之后的 op1378 通知与 op1445
//     "开始播放"推进与加开关前逐字节一致；
//   - 开关 false 时选角路径零下发 op1378、op1445 上报既不推进也不持久化
//     story_digest_last_level，且两条被抑制的路径都记一条
//     EVENT kind=game-story-digest-disabled。

const (
	// storyDigestSwitchLegacy 按加开关之前的写法构造 options：完全不提新字段。
	storyDigestSwitchLegacy = "legacy"
	// storyDigestSwitchEnabled 显式写入"开启"（与 legacy 必须逐字节相同）。
	storyDigestSwitchEnabled = "enabled"
	// storyDigestSwitchDisabled 显式写入"关闭"。
	storyDigestSwitchDisabled = "disabled"
)

// storyDigestSwitchCharacterID 是夹具角色；沿用 story_digest_test.go 里
// "老角色已完成教程"的档位，使选角路径在 op1378 之后不再发任何场景包。
const storyDigestSwitchCharacterID = "77"

type storyDigestSwitchFixture struct {
	service      *Service
	repositories dnfrepo.Group
	session      *gameSession
}

func storyDigestSwitchOptions(mode string) options {
	opts := options{accountPrefix: defaultAccountPrefix}
	switch mode {
	case storyDigestSwitchEnabled:
		opts.storyDigestDisabled = false
	case storyDigestSwitchDisabled:
		opts.storyDigestDisabled = true
	}
	return opts
}

func newStoryDigestSwitchFixture(t *testing.T, mode string) storyDigestSwitchFixture {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: storyDigestSwitchCharacterID,
		AccountID:   defaultAccountPrefix + "1",
		Slot:        0,
		Name:        "legacy",
		Job:         "0",
		Level:       35,
		Stats: map[string]int64{
			currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag,
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            storyDigestSwitchOptions(mode),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	return storyDigestSwitchFixture{
		service:      service,
		repositories: repositories,
		session:      newStoryDigestTestSession(),
	}
}

// selectWire 走一遍选角（sendUpperCSharpSelectInit），返回该路径写在连接上的
// 原始字节：默认档是"select ack + op1378"，关闭档应只剩 select ack。
func (fixture storyDigestSwitchFixture) selectWire(t *testing.T) []byte {
	t.Helper()
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, 77)
	if err := fixture.service.sendUpperCSharpSelectInit(fixture.session, request); err != nil {
		t.Fatalf("select init: %v", err)
	}
	return append([]byte(nil), fixture.session.conn.(*bufferConn).write.Bytes()...)
}

func (fixture storyDigestSwitchFixture) loadCharacter(t *testing.T) dnfrepo.CharacterRecord {
	t.Helper()
	record, found, err := fixture.repositories.Character.Load(context.Background(), storyDigestSwitchCharacterID)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	return record
}

func storyDigestPlaybackStartFrame(t *testing.T) []byte {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketStoryDigestUpdate),
		nil,
		0,
		dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestStoryDigestOptionReadsEnvironmentSwitchAndDefaultsToEnabled(t *testing.T) {
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
		t.Run("DNFBRIDGE_STORY_DIGEST_ENABLED="+test.value, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_STORY_DIGEST_ENABLED", test.value)
			service := &Service{options: optionsFromConfig(config.ServiceConfig{})}
			if got := service.storyDigestEnabled(); got != test.want {
				t.Fatalf("story digest enabled=%t want=%t", got, test.want)
			}
		})
	}
	// 零值 options 是加开关之前的构造方式（测试与探针里的 Service 字面量），
	// 它必须保持剧情回顾开启，否则"默认 true 行为不变"就不成立。
	if !(&Service{}).storyDigestEnabled() {
		t.Fatal("zero-value options disabled the story digest subsystem")
	}
	if (*Service)(nil).storyDigestEnabled() {
		t.Fatal("nil service reported the story digest as enabled")
	}
	if (&Service{options: options{storyDigestDisabled: true}}).storyDigestEnabled() {
		t.Fatal("explicitly disabled story digest reported as enabled")
	}
}

func TestStoryDigestSwitchSelectWireMatchesLegacyWhenEnabledAndDropsOp1378WhenDisabled(t *testing.T) {
	legacy := newStoryDigestSwitchFixture(t, storyDigestSwitchLegacy)
	enabled := newStoryDigestSwitchFixture(t, storyDigestSwitchEnabled)
	disabled := newStoryDigestSwitchFixture(t, storyDigestSwitchDisabled)
	if !legacy.service.storyDigestEnabled() || !enabled.service.storyDigestEnabled() {
		t.Fatal("default or explicitly enabled story digest is not active")
	}
	if disabled.service.storyDigestEnabled() {
		t.Fatal("disabled story digest is active")
	}

	legacyWire := legacy.selectWire(t)
	if enabledWire := enabled.selectWire(t); !bytes.Equal(legacyWire, enabledWire) {
		t.Fatalf("enabled wire=%x legacy wire=%x", enabledWire, legacyWire)
	}
	if len(legacyWire) == 0 {
		t.Fatal("enabled story digest wrote no packets")
	}
	ack, rest := splitGameServerUpperPacket(t, legacyWire)
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketSelectCharacter) {
		t.Fatalf("first packet=%+v want select ack", ack.Header)
	}
	ackLen := len(legacyWire) - len(rest)
	rest = requireCurrentStoryDigestLastLevelPacket(t, rest, 0)
	if len(rest) != 0 {
		t.Fatalf("default select route emitted trailing packets=%x", rest)
	}

	disabledWire := disabled.selectWire(t)
	if bytes.Equal(disabledWire, legacyWire) {
		t.Fatal("disabled story digest still wrote the op1378 notification")
	}
	// 与默认档的唯一差别就是少了 op1378 那一帧：ack 帧本身逐字节相同。
	if !bytes.Equal(disabledWire, legacyWire[:ackLen]) {
		t.Fatalf("disabled wire=%x want legacy select ack only=%x", disabledWire, legacyWire[:ackLen])
	}
	disabledAck, trailing := splitGameServerUpperPacket(t, disabledWire)
	if disabledAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectCharacter) || len(trailing) != 0 {
		t.Fatalf("disabled select route ack=%+v trailing=%x", disabledAck.Header, trailing)
	}
}

func TestStoryDigestSwitchDisabledKeepsStatAndWritesNothingOnPlaybackStart(t *testing.T) {
	ctx := context.Background()
	disabled := newStoryDigestSwitchFixture(t, storyDigestSwitchDisabled)
	disabled.session.selectedCharacterID = 77
	connection := disabled.session.conn.(*bufferConn)

	// 当前 EXE 的 class-1 upper 传输路径。
	if err := disabled.service.handleGameUpper(disabled.session, storyDigestPlaybackStartFrame(t)); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("disabled op1445 fabricated a response=%x", connection.write.Bytes())
	}
	// 老的 legacy 游戏解码路径（同一 owner，见 game_dispatch_legacy.go）。
	if err := disabled.service.handleGameCommand(
		disabled.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketStoryDigestUpdate),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("disabled legacy op1445 fabricated a response=%x", connection.write.Bytes())
	}
	record, found, err := disabled.repositories.Character.Load(ctx, storyDigestSwitchCharacterID)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if record.Stats[dnfrepo.CharacterStoryDigestLastLevelStatKey] != 0 ||
		record.Stats[dnfrepo.CharacterStoryDigestMigrationVersionStatKey] != 0 {
		t.Fatalf("disabled op1445 mutated the story state=%#v", record.Stats)
	}

	// 默认（未设置开关）下同一条上报照旧推进并落库：差异只来自开关这一个变量。
	legacy := newStoryDigestSwitchFixture(t, storyDigestSwitchLegacy)
	legacy.session.selectedCharacterID = 77
	if err := legacy.service.handleGameUpper(legacy.session, storyDigestPlaybackStartFrame(t)); err != nil {
		t.Fatal(err)
	}
	stored := legacy.loadCharacter(t)
	if stored.Stats[dnfrepo.CharacterStoryDigestLastLevelStatKey] != 35 ||
		stored.Stats[dnfrepo.CharacterStoryDigestMigrationVersionStatKey] != int64(dnfrepo.CurrentCharacterStoryDigestMigrationVersion) {
		t.Fatalf("default op1445 did not advance the story state=%#v", stored.Stats)
	}
	if legacy.session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("default op1445 changed the response shape=%x", legacy.session.conn.(*bufferConn).write.Bytes())
	}
}

func TestStoryDigestSwitchDisabledLogsEverySuppressedPath(t *testing.T) {
	readLog := func(t *testing.T, mode string) string {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), "packet-"+mode+".log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		fixture := newStoryDigestSwitchFixture(t, mode)
		fixture.service.packetLog = logger
		fixture.selectWire(t)
		fixture.session.selectedCharacterID = 77
		if err := fixture.service.handleGameUpper(fixture.session, storyDigestPlaybackStartFrame(t)); err != nil {
			t.Fatal(err)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(logged)
	}

	text := readLog(t, storyDigestSwitchDisabled)
	for _, want := range []string{
		"EVENT kind=game-story-digest-disabled",
		"source=select_character_after_ack_before_scene",
		"source=op1445_accepted_playback_start",
		"reason=story_digest_disabled",
		"switch=DNFBRIDGE_STORY_DIGEST_ENABLED",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("disabled log missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "kind=game-current-story-digest-last-level-send") ||
		strings.Contains(text, "kind=game-current-story-digest-advanced") {
		t.Fatalf("disabled log still recorded a story digest send/advance: %q", text)
	}

	// 默认档既不下发抑制事件，也照旧记录下发与推进：开关是唯一变量。
	legacyText := readLog(t, storyDigestSwitchLegacy)
	if strings.Contains(legacyText, "game-story-digest-disabled") {
		t.Fatalf("default log recorded a disabled event: %q", legacyText)
	}
	if !strings.Contains(legacyText, "kind=game-current-story-digest-last-level-send") ||
		!strings.Contains(legacyText, "kind=game-current-story-digest-advanced") {
		t.Fatalf("default log lost the story digest send/advance evidence: %q", legacyText)
	}
}
