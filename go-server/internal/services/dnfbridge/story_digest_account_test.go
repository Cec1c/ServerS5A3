package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是"账号级剧情回顾记录"的聚焦用例：账号级记录只加一条元数据键
// (story_digest_viewed_level)，但选角下发与 op1445 推进都要按它收敛，并且
// DNFBRIDGE_STORY_DIGEST_ENABLED=0 的抑制行为逐字节不变。

type storyDigestAccountFixture struct {
	service      *Service
	repositories dnfrepo.Group
	session      *gameSession
}

// newStoryDigestAccountFixture 构造夹具：角色等级/角色自己的 story 记录、账号
// 记录（nil 表示该账号在仓储里不存在）、以及开关档位都显式给。
func newStoryDigestAccountFixture(
	t *testing.T,
	characterID string,
	level int,
	characterStoryLevel int64,
	accountViewedLevel *uint32,
	disabled bool,
) storyDigestAccountFixture {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	stats := map[string]int64{
		currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag,
	}
	if characterStoryLevel > 0 {
		stats[dnfrepo.CharacterStoryDigestLastLevelStatKey] = characterStoryLevel
		stats[dnfrepo.CharacterStoryDigestMigrationVersionStatKey] = int64(dnfrepo.CurrentCharacterStoryDigestMigrationVersion)
	}
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   defaultAccountPrefix + "1",
		Slot:        0,
		Name:        "story-account",
		Job:         "0",
		Level:       level,
		Stats:       stats,
	}); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]string{"selector_adventure_info_slot": "4"}
	if accountViewedLevel != nil {
		metadata[dnfrepo.AccountStoryDigestViewedLevelMetadataKey] =
			dnfrepo.FormatAccountStoryDigestViewedLevel(*accountViewedLevel)
	}
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: defaultAccountPrefix + "1",
		State:     "open",
		Metadata:  metadata,
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix, storyDigestDisabled: disabled},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	return storyDigestAccountFixture{
		service:      service,
		repositories: repositories,
		session:      newStoryDigestTestSession(),
	}
}

// selectWire 走一遍选角（sendUpperCSharpSelectInit），返回写在连接上的原始字节。
func (fixture storyDigestAccountFixture) selectWire(t *testing.T, characterID uint16) []byte {
	t.Helper()
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, characterID)
	if err := fixture.service.sendUpperCSharpSelectInit(fixture.session, request); err != nil {
		t.Fatalf("select init: %v", err)
	}
	return append([]byte(nil), fixture.session.conn.(*bufferConn).write.Bytes()...)
}

func (fixture storyDigestAccountFixture) loadAccountViewedLevel(t *testing.T) (uint32, bool) {
	t.Helper()
	account, found, err := fixture.repositories.Account.Load(context.Background(), defaultAccountPrefix+"1")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	_, hasKey := account.Metadata[dnfrepo.AccountStoryDigestViewedLevelMetadataKey]
	return dnfrepo.AccountStoryDigestViewedLevel(account), hasKey
}

func (fixture storyDigestAccountFixture) loadCharacterStoryLevel(t *testing.T, characterID string) int64 {
	t.Helper()
	record, found, err := fixture.repositories.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	return record.Stats[dnfrepo.CharacterStoryDigestLastLevelStatKey]
}

// readEventLog 在临时目录里跑一遍回调，返回 packet log 的全文。
func readEventLog(
	t *testing.T,
	name string,
	fixture storyDigestAccountFixture,
	run func(fixture *storyDigestAccountFixture),
) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "packet-"+name+".log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.packetLog = logger
	run(&fixture)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

func TestStoryDigestSendUsesAccountViewedLevelForLaggingCharacter(t *testing.T) {
	// 现场形态：角色被 GM 拔到 90，角色自己的记录还停在 40（客户端认为落后 ⇒
	// 每次登录强制播放）；账号级记录已经到 90 ⇒ 必须下发 90。
	viewed := uint32(90)
	fixture := newStoryDigestAccountFixture(t, "77", 90, 40, &viewed, false)
	wire := fixture.selectWire(t, 77)
	_, rest := splitGameServerUpperPacket(t, wire)
	rest = requireCurrentStoryDigestLastLevelPacket(t, rest, 90)
	if len(rest) != 0 {
		t.Fatalf("select route emitted trailing packets: %x", rest)
	}
}

func TestStoryDigestSendClampsToCharacterLevelWhenAccountAlreadyWatched(t *testing.T) {
	// 账号级记录 90 高于本角色等级 40 ⇒ 直接下发角色当前等级（"全都看过"），
	// 而不是把 90 发下去。
	viewed := uint32(90)
	fixture := newStoryDigestAccountFixture(t, "78", 40, 40, &viewed, false)
	wire := fixture.selectWire(t, 78)
	_, rest := splitGameServerUpperPacket(t, wire)
	rest = requireCurrentStoryDigestLastLevelPacket(t, rest, 40)
	if len(rest) != 0 {
		t.Fatalf("select route emitted trailing packets: %x", rest)
	}
}

func TestStoryDigestSendKeepsLegacyBodyWithoutAccountViewedRecord(t *testing.T) {
	// 账号记录里没有这个键（老账号 / 刚部署还没播种 / 内存后端）：下发必须与
	// 加账号记录之前一致，只按角色自己的 story_digest_last_level 走。
	fixture := newStoryDigestAccountFixture(t, "79", 90, 40, nil, false)
	wire := fixture.selectWire(t, 79)
	_, rest := splitGameServerUpperPacket(t, wire)
	rest = requireCurrentStoryDigestLastLevelPacket(t, rest, 40)
	if len(rest) != 0 {
		t.Fatalf("select route emitted trailing packets: %x", rest)
	}
	empty := uint32(0)
	fixture = newStoryDigestAccountFixture(t, "80", 90, 40, &empty, false)
	wire = fixture.selectWire(t, 80)
	_, rest = splitGameServerUpperPacket(t, wire)
	rest = requireCurrentStoryDigestLastLevelPacket(t, rest, 40)
	if len(rest) != 0 {
		t.Fatalf("zero account record emitted trailing packets: %x", rest)
	}
}

func TestStoryDigestSendLogsAccountSentAndCharacterLevels(t *testing.T) {
	viewed := uint32(90)
	text := readEventLog(t, "send", newStoryDigestAccountFixture(t, "77", 90, 40, &viewed, false),
		func(fixture *storyDigestAccountFixture) {
			fixture.selectWire(t, 77)
		})
	for _, want := range []string{
		"kind=game-current-story-digest-last-level-send",
		"account_viewed_level=90",
		"sent_level=90",
		"char_level=90",
		"character_story_digest_last_level=40",
		"account_record_found=true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("send log missing %q: %q", want, text)
		}
	}
}

func TestStoryDigestPlaybackStartAdvancesCharacterAndAccountRecords(t *testing.T) {
	viewed := uint32(0)
	fixture := newStoryDigestAccountFixture(t, "77", 35, 0, &viewed, false)
	fixture.session.selectedCharacterID = 77
	if err := fixture.service.handleGameUpper(fixture.session, storyDigestPlaybackStartFrame(t)); err != nil {
		t.Fatal(err)
	}
	if fixture.session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("op1445 fabricated a response: %x", fixture.session.conn.(*bufferConn).write.Bytes())
	}
	if got := fixture.loadCharacterStoryLevel(t, "77"); got != 35 {
		t.Fatalf("character story level = %d, want 35", got)
	}
	accountLevel, hasKey := fixture.loadAccountViewedLevel(t)
	if !hasKey || accountLevel != 35 {
		t.Fatalf("account viewed level = %d (key=%t), want 35", accountLevel, hasKey)
	}
	account, _, err := fixture.repositories.Account.Load(context.Background(), defaultAccountPrefix+"1")
	if err != nil {
		t.Fatal(err)
	}
	if account.Metadata["selector_adventure_info_slot"] != "4" {
		t.Fatalf("unrelated account metadata lost: %#v", account.Metadata)
	}

	// 重放同一条上报：幂等，不改变两个记录，也不产生第二包。
	for attempt := 0; attempt < 2; attempt++ {
		if err := fixture.service.handleGameUpper(fixture.session, storyDigestPlaybackStartFrame(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.loadCharacterStoryLevel(t, "77"); got != 35 {
		t.Fatalf("replayed character story level = %d, want 35", got)
	}
	if accountLevel, _ := fixture.loadAccountViewedLevel(t); accountLevel != 35 {
		t.Fatalf("replayed account viewed level = %d, want 35", accountLevel)
	}
}

func TestStoryDigestPlaybackStartNeverLowersAccountViewedLevel(t *testing.T) {
	viewed := uint32(90)
	fixture := newStoryDigestAccountFixture(t, "77", 35, 0, &viewed, false)
	fixture.session.selectedCharacterID = 77
	if err := fixture.service.handleGameUpper(fixture.session, storyDigestPlaybackStartFrame(t)); err != nil {
		t.Fatal(err)
	}
	if got := fixture.loadCharacterStoryLevel(t, "77"); got != 35 {
		t.Fatalf("character story level = %d, want 35", got)
	}
	if accountLevel, _ := fixture.loadAccountViewedLevel(t); accountLevel != 90 {
		t.Fatalf("account viewed level = %d, want it to stay 90", accountLevel)
	}
}

func TestStoryDigestPlaybackStartLogsAccountAdvanceFields(t *testing.T) {
	viewed := uint32(0)
	text := readEventLog(t, "advance", newStoryDigestAccountFixture(t, "77", 90, 40, &viewed, false),
		func(fixture *storyDigestAccountFixture) {
			fixture.session.selectedCharacterID = 77
			if err := fixture.service.handleGameUpper(fixture.session, storyDigestPlaybackStartFrame(t)); err != nil {
				t.Fatal(err)
			}
		})
	for _, want := range []string{
		"kind=game-current-story-digest-advanced",
		"account_viewed_level=90",
		"sent_level=90",
		"char_level=90",
		"previous_account_viewed_level=0",
		"account_advanced=true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("advance log missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "game-current-story-digest-account-advance-failed") {
		t.Fatalf("account advance reported a failure: %q", text)
	}
}

func TestStoryDigestPlaybackStartLogsAccountAdvanceFailure(t *testing.T) {
	// 账号级写入失败必须留事件（不静默），但不改变 op1445 的零回包形状。
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   defaultAccountPrefix + "1",
		Level:       35,
		Stats: map[string]int64{
			currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 账号记录不存在 ⇒ memory 仓储返回 ErrAccountStoryDigestAccountMissing。
	session := newStoryDigestTestSession()
	session.selectedCharacterID = 77
	logPath := filepath.Join(t.TempDir(), "packet-account-missing.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:          logger,
	}
	if err := service.handleGameUpper(session, storyDigestPlaybackStartFrame(t)); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("op1445 fabricated a response: %x", session.conn.(*bufferConn).write.Bytes())
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, want := range []string{
		"kind=game-current-story-digest-account-advance-failed",
		"account_viewed_level=0",
		"sent_level=35",
		"char_level=35",
		"dnf account story digest account is missing",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("failure log missing %q: %q", want, text)
		}
	}
	record, found, err := repositories.Character.Load(context.Background(), "77")
	if err != nil || !found || record.Stats[dnfrepo.CharacterStoryDigestLastLevelStatKey] != 35 {
		t.Fatalf("character record not advanced found=%t err=%v record=%#v", found, err, record)
	}
}

func TestStoryDigestSwitchDisabledLeavesAccountRecordUntouched(t *testing.T) {
	// 关闭档：账号记录不存在，选角零下发 op1378，op1445 也不写账号记录。
	disabled := newStoryDigestAccountFixture(t, "77", 90, 40, nil, true)
	wire := disabled.selectWire(t, 77)
	ack, rest := splitGameServerUpperPacket(t, wire)
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketSelectCharacter) {
		t.Fatalf("first packet=%+v", ack.Header)
	}
	if len(rest) != 0 {
		t.Fatalf("disabled select route still emitted op1378: %x", rest)
	}

	disabled.session.selectedCharacterID = 77
	if err := disabled.service.handleGameUpper(disabled.session, storyDigestPlaybackStartFrame(t)); err != nil {
		t.Fatal(err)
	}
	if accountLevel, hasKey := disabled.loadAccountViewedLevel(t); hasKey || accountLevel != 0 {
		t.Fatalf("disabled story digest wrote the account-level record: level=%d key=%t", accountLevel, hasKey)
	}
	if got := disabled.loadCharacterStoryLevel(t, "77"); got != 40 {
		t.Fatalf("disabled op1445 moved the character record to %d, want 40", got)
	}
}

func TestStoryDigestSwitchDisabledLogsNoAccountEvents(t *testing.T) {
	text := readEventLog(t, "disabled", newStoryDigestAccountFixture(t, "77", 90, 40, nil, true),
		func(fixture *storyDigestAccountFixture) {
			fixture.selectWire(t, 77)
			fixture.session.selectedCharacterID = 77
			if err := fixture.service.handleGameUpper(fixture.session, storyDigestPlaybackStartFrame(t)); err != nil {
				t.Fatal(err)
			}
		})
	for _, want := range []string{
		"kind=game-story-digest-disabled",
		"source=select_character_after_ack_before_scene",
		"source=op1445_accepted_playback_start",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("disabled log missing %q: %q", want, text)
		}
	}
	for _, forbidden := range []string{
		"kind=game-current-story-digest-last-level-send",
		"kind=game-current-story-digest-advanced",
		"account_viewed_level",
		"account_record_found",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("disabled log unexpectedly contains %q: %q", forbidden, text)
		}
	}
}
