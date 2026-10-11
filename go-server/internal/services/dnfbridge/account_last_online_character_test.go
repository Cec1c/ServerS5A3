package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// D33 反馈13（续）：好友离线要显示"上次在线角色"。为此在成功选角时把账号最后
// 选中的角色写进账号 metadata 的键级字段（沿用既有 SaveAccountMetadataEntry，
// 不新增数据库列）。下列测试覆盖：成功选角写入、重选/旧 owner 不覆盖、账号隔离、
// 冷服务离线恢复。

func accountMetadataValue(t *testing.T, repositories dnfrepo.Group, accountID, key string) string {
	t.Helper()
	record, found, err := repositories.Account.Load(context.Background(), accountID)
	if err != nil || !found {
		t.Fatalf("load account %s: found=%v err=%v", accountID, found, err)
	}
	return strings.TrimSpace(record.Metadata[key])
}

func setAccountMetadataValue(t *testing.T, repositories dnfrepo.Group, accountID, key, value string) {
	t.Helper()
	record, found, err := repositories.Account.Load(context.Background(), accountID)
	if err != nil || !found {
		t.Fatalf("load account %s: found=%v err=%v", accountID, found, err)
	}
	if record.Metadata == nil {
		record.Metadata = map[string]string{}
	}
	if value == "" {
		delete(record.Metadata, key)
	} else {
		record.Metadata[key] = value
	}
	if err := repositories.Account.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

// 只有真正走完选角入口（UpperMsgSelectCharacter）才会写下记录：选角前该账号还没有这条记录。
func TestLastOnlineCharacterPersistedOnSuccessfulSelection(t *testing.T) {
	s, session, repositories := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	session.accountID, session.accountFromToken, session.loginPacketSeen = "dnf:1", true, true
	session.selectedCharacterID = 0
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("load character: found=%v err=%v", found, err)
	}
	character.Name, character.Job, character.Level = "owner", "0", 90
	character.Stats["grow_type"] = 33
	character.Stats[currentDungeonTutorialCompletedKey] = currentDungeonTutorialCompleteFlag
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if record, found, err := repositories.Account.Load(ctx, "dnf:1"); err == nil && found && strings.TrimSpace(record.Metadata[lastOnlineCharacterMetadataKey]) != "" {
		t.Fatalf("record written before selection: %q", record.Metadata[lastOnlineCharacterMetadataKey])
	}
	roster, err := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgCharacterRoster), nil, 0, proto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, roster); err != nil {
		t.Fatal(err)
	}
	var selected [2]byte
	binary.LittleEndian.PutUint16(selected[:], 29)
	selection, err := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), selected[:], 0, proto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, selection); err != nil {
		t.Fatal(err)
	}
	if got := accountMetadataValue(t, repositories, "dnf:1", lastOnlineCharacterMetadataKey); got != "29" {
		t.Fatalf("last online character=%q want 29", got)
	}
}

// 重选不被旧 owner 覆盖，且写入严格按账号隔离。
func TestLastOnlineCharacterRejectsSupersededOwnerAndIsolatesAccounts(t *testing.T) {
	s, _, _, _, _ := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	// 独立账号/角色：避开夹具里已有的在线会话（resident owner）对绑定的影响。
	const accountID = "dnf:9"
	if err := g.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID, RepresentAccountName: "Nine", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"90": "nine-a", "91": "nine-b"} {
		if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: accountID, Name: name, Job: "0", Level: 90}); err != nil {
			t.Fatal(err)
		}
	}
	newSession := func() *gameSession {
		return &gameSession{conn: &bufferConn{}, accountID: accountID, loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 11}}
	}
	// 角色 90 被选中。
	first := newSession()
	s.bindGameSessionCharacter(first, 90)
	s.rememberLastOnlineCharacter(ctx, first, 90)
	if got := accountMetadataValue(t, g, accountID, lastOnlineCharacterMetadataKey); got != "90" {
		t.Fatalf("first selection record=%q want 90", got)
	}
	// 账号隔离：dnf:1 从未选角，不得出现这条记录。
	if got := accountMetadataValue(t, g, "dnf:1", lastOnlineCharacterMetadataKey); got != "" {
		t.Fatalf("record leaked into another account: %q", got)
	}
	// 同一账号改选另一个角色 91。
	second := newSession()
	s.bindGameSessionCharacter(second, 91)
	s.rememberLastOnlineCharacter(ctx, second, 91)
	if got := accountMetadataValue(t, g, accountID, lastOnlineCharacterMetadataKey); got != "91" {
		t.Fatalf("re-selection record=%q want 91", got)
	}
	// 角色 90 被代次更新的 owner 接管；旧 owner 再写一次不得覆盖 91。
	replacement := newSession()
	s.bindGameSessionCharacter(replacement, 90)
	s.rememberLastOnlineCharacter(ctx, first, 90)
	if got := accountMetadataValue(t, g, accountID, lastOnlineCharacterMetadataKey); got != "91" {
		t.Fatalf("superseded owner overwrote the record: %q want 91", got)
	}
	// 当前 owner 的写入仍然生效。
	s.rememberLastOnlineCharacter(ctx, replacement, 90)
	if got := accountMetadataValue(t, g, accountID, lastOnlineCharacterMetadataKey); got != "90" {
		t.Fatalf("current owner write ignored: %q want 90", got)
	}
}

// 冷服务（新进程语义）只靠持久记录恢复：接受时刻角色是 5，最后选中的是 8。
func TestLastOnlineCharacterRestoredFromColdServiceOffline(t *testing.T) {
	s, a, b, _, _ := prepareAccountFriendTest(t)
	ctx := context.Background()
	callAccountFriend(t, s, a, 1436, "Beta", "申请", 0)
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)
	last := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 9}}
	s.bindGameSessionCharacter(last, 8)
	s.rememberLastOnlineCharacter(ctx, last, 8)
	s.unbindGameSession(last)
	s.unbindGameSession(b)
	cold := &Service{options: s.options, repositoryProvider: s.repositoryProvider}
	rows, _, err := cold.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "小号" || rows[0].Channel != 0 {
		t.Fatalf("cold service did not restore the last-online character: rows=%+v err=%v", rows, err)
	}
}

// legacy class0 选角入口（GameTypeSelectCharacter）同样写入该记录。
func TestLastOnlineCharacterPersistedOnLegacySelectCharacterState(t *testing.T) {
	s, session, repositories := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	session.accountID, session.accountFromToken, session.loginPacketSeen = "dnf:1", true, true
	session.selectedCharacterID = 0
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("load character: found=%v err=%v", found, err)
	}
	character.Slot = 0
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	// NoPack plain slot request: u32 slot(0) + 12 zero bytes.
	body := make([]byte, 16)
	if err := s.sendSelectCharacterState(session, body); err != nil {
		t.Fatal(err)
	}
	if got := accountMetadataValue(t, repositories, "dnf:1", lastOnlineCharacterMetadataKey); got != "29" {
		t.Fatalf("legacy select did not record the last character: %q want 29", got)
	}
}

// 旧选角的写在 repo 内被阻塞、新选角先落地；旧写放行后不得覆盖新值（账号锁内比较已应用
// generation 的单进程序列门控）。
func TestLastOnlineCharacterRejectsOlderInFlightWrite(t *testing.T) {
	s, _, b, _, _ := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	// 释放夹具占用的角色 5，避免同角色 resident owner 让新绑定早退。
	s.unbindGameSession(b)
	old := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 5}}
	s.bindGameSessionCharacter(old, 5)
	newer := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 6}}
	s.bindGameSessionCharacter(newer, 8)
	entered := make(chan struct{})
	release := make(chan struct{})
	wrapped := g
	wrapped.Character = &gatedCharacterRepository{CharacterRepository: g.Character, blockID: "5", entered: entered, release: release}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return wrapped, true }
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.rememberLastOnlineCharacter(ctx, old, 5)
	}()
	<-entered // 旧选角已取到代次，且阻塞在 repo 读取里
	s.rememberLastOnlineCharacter(ctx, newer, 8)
	if got := accountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey); got != "8" {
		t.Fatalf("newer selection not recorded before releasing the older write: %q", got)
	}
	close(release)
	<-done
	if got := accountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey); got != "8" {
		t.Fatalf("older in-flight write clobbered the newer selection: %q want 8", got)
	}
}

// gatedCharacterRepository 让指定角色的 Load 在 repo 内阻塞一次，复现 TOCTOU 窗口。
type gatedCharacterRepository struct {
	dnfrepo.CharacterRepository
	blockID string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedCharacterRepository) Load(ctx context.Context, characterID string) (dnfrepo.CharacterRecord, bool, error) {
	if characterID == g.blockID {
		g.once.Do(func() {
			close(g.entered)
			<-g.release
		})
	}
	return g.CharacterRepository.Load(ctx, characterID)
}

// bind 顺序与 remember 调用顺序相反时，仍必须保留后 bind 的新选角（不需要任何 IO 巧合）。
func TestLastOnlineCharacterKeepsNewerBindGeneration(t *testing.T) {
	s, _, b, _, _ := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	// 释放夹具占用的角色 5，避免同角色 resident owner 让新绑定早退。
	s.unbindGameSession(b)
	older := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 5}}
	s.bindGameSessionCharacter(older, 5)
	newer := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 6}}
	s.bindGameSessionCharacter(newer, 8)
	if newer.characterGeneration <= older.characterGeneration {
		t.Fatalf("fixture: later bind generation %d must exceed %d", newer.characterGeneration, older.characterGeneration)
	}
	// 调用顺序反转：新角色先 remember，旧角色后 remember（旧 session 的 IO/调度更晚）。
	s.rememberLastOnlineCharacter(ctx, newer, 8)
	s.rememberLastOnlineCharacter(ctx, older, 5)
	if got := accountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey); got != "8" {
		t.Fatalf("older bind generation overwrote the newer selection: %q want 8", got)
	}
}
