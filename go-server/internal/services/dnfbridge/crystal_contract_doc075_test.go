package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// DOC075 (前身 P62)：晶体契约选择跨角色串用。
//
// 桥侧把选择持久化在角色自己的 Stats[premium_crystal_selection] 上
// (crystalcontract/owner.go)，而当前客户端只有一个进程级选择缓存
// (DNF.exe sha256 1ac12645...fd4331 -> 0x5590CF0)，其唯一的服务端写入点
// 是 op898 处理器里的 .cryssel detour。因此服务端这一侧的契约是：
// **每次进入角色都必须按当前角色重放一次状态**，同一条连接换角色后也要重放，
// 且包形必须与客户端处理器读取的字节一致：
//   - op535 ACK   ：class1 信封 {success=1, sourceFlag, selection}
//   - op898 状态  ：class0 原始通知 {unusedFlag, selection}（0xFF = 未选择）
//
// 这里同时修正上一版临时探针的标签错位：旧探针在角色 21 选择时读的是**角色 19 的
// 连接**，所以日志里的 "B ack" 其实是错位的包；本文件所有读取都走会话自己的连接，
// 断言按包类型（class+msgID)驱动，而不是按读取顺序猜标签。
//
// 客户端为什么看不到：修正后的门控探针（crystal-evidence/probe_gate.py）在真实
// v101 EXE 上对全部 65536 个 opcode 求值，0x382(898) 与 0x217(535) 的准入位都是 0，
// 包在分发前被丢弃。那一侧不是本仓库可以修的东西，属于客户端白名单候选的决定点。
func newDoc075TwoCharacterCrystalRuntime(
	t *testing.T,
) (*Service, *gameSession, dnfrepo.Group, *bufferConn) {
	t.Helper()
	service, session, repositories, connection := newCrystalContractTestRuntime(t, map[string]dnfrepo.ItemStack{
		"0:354": {ItemID: 3033, Count: 5},
		"0:355": {ItemID: 3034, Count: 5},
	})
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "21",
		AccountID:   "account-1",
		Stats:       map[string]int64{},
	}); err != nil {
		t.Fatal(err)
	}
	return service, session, repositories, connection
}

func doc075Session(connection *bufferConn, connID string, characterID uint16) *gameSession {
	return &gameSession{
		conn:                connection,
		connID:              connID,
		selectedCharacterID: characterID,
		accountID:           "account-1",
	}
}

func doc075Connection(t *testing.T, session *gameSession) *bufferConn {
	t.Helper()
	connection, ok := session.conn.(*bufferConn)
	if !ok || connection == nil {
		t.Fatalf("session %s has no buffer connection", session.connID)
	}
	return connection
}

// doc075SelectCrystal drives one op535 selection request and asserts both the
// ACK and the state replay on that session's own connection.
func doc075SelectCrystal(t *testing.T, service *Service, session *gameSession, selection byte) {
	t.Helper()
	if err := service.handleCurrentCrystalContractUpdate(session, []byte{0, selection}); err != nil {
		t.Fatalf("select crystal %d: %v", selection, err)
	}
	connection := doc075Connection(t, session)
	ack, rest := nextCrystalPacket(t, connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketUpdateContractOfCubeInfo) ||
		!bytes.Equal(ack.Body, []byte{1, 0, selection}) {
		t.Fatalf("op535 ack header=%+v body=%x, want class1/op535 {success,sourceFlag,selection=%d}",
			ack.Header, ack.Body, selection)
	}
	state, trailing := nextCrystalPacket(t, rest)
	assertCurrentCrystalStatePacket(t, state, []byte{0, selection})
	if len(trailing) != 0 {
		t.Fatalf("op898 replay after op535 ack trailing=%x", trailing)
	}
	connection.write.Reset()
}

func doc075RequireState(t *testing.T, service *Service, session *gameSession, want byte) {
	t.Helper()
	if err := service.sendCurrentCrystalContractState(session, "doc075_require_state"); err != nil {
		t.Fatalf("send crystal state: %v", err)
	}
	connection := doc075Connection(t, session)
	state, trailing := nextCrystalPacket(t, connection.write.Bytes())
	assertCurrentCrystalStatePacket(t, state, []byte{0, want})
	if len(trailing) != 0 {
		t.Fatalf("op898 state trailing=%x", trailing)
	}
	connection.write.Reset()
}

func doc075StoredSelection(t *testing.T, repositories dnfrepo.Group, characterID string) int64 {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s found=%t err=%v", characterID, found, err)
	}
	return character.Stats["premium_crystal_selection"]
}

func TestDoc075CrystalSelectionIsPerCharacterAcrossFreshLogins(t *testing.T) {
	service, sessionA, repositories, _ := newDoc075TwoCharacterCrystalRuntime(t)

	doc075SelectCrystal(t, service, sessionA, 0)

	// Character 21 enters for the first time on the same account and must not
	// inherit character 19's black selection.
	freshB := doc075Session(&bufferConn{}, "doc075-fresh-b", 21)
	doc075RequireState(t, service, freshB, 0xff)
	doc075SelectCrystal(t, service, freshB, 1)

	// Both characters re-enter later and still replay their own selection.
	doc075RequireState(t, service, doc075Session(&bufferConn{}, "doc075-relog-a", 19), 0)
	doc075RequireState(t, service, doc075Session(&bufferConn{}, "doc075-relog-b", 21), 1)

	if got := doc075StoredSelection(t, repositories, "19"); got != 0 {
		t.Fatalf("stored selection for character 19 = %d, want 0", got)
	}
	if got := doc075StoredSelection(t, repositories, "21"); got != 1 {
		t.Fatalf("stored selection for character 21 = %d, want 1", got)
	}
}

func TestDoc075CharacterSwitchReplaysNewCharacterStateOnce(t *testing.T) {
	service, session, repositories, connection := newDoc075TwoCharacterCrystalRuntime(t)
	doc075SelectCrystal(t, service, session, 0)

	// op7 closes character 19 and returns the client to the character selector.
	if err := service.handleUpperReturnSelectCharacter(session, nil); err != nil {
		t.Fatalf("op7 return select: %v", err)
	}
	if session.selectedCharacterID != 0 {
		t.Fatalf("selected character after op7 = %d, want 0", session.selectedCharacterID)
	}
	connection.write.Reset()

	// The selector enters character 21 on the same connection: the per-character
	// replay gate must be cleared again, and the replay must describe character 21
	// (nothing selected yet) instead of repeating character 19's black selection.
	service.bindGameSessionCharacter(session, 21)
	service.armCurrentInitialTownRoute(session, 21)

	if err := service.sendCurrentCrystalContractTownUIReadyState(session); err != nil {
		t.Fatalf("character-switch town ui-ready crystal state: %v", err)
	}
	state, trailing := nextCrystalPacket(t, connection.write.Bytes())
	assertCurrentCrystalStatePacket(t, state, []byte{0, 0xff})
	if len(trailing) != 0 {
		t.Fatalf("character-switch op898 state trailing=%x", trailing)
	}
	connection.write.Reset()

	// One character entry replays the durable state once, not once per boundary.
	if err := service.sendCurrentCrystalContractTownUIReadyState(session); err != nil {
		t.Fatalf("repeated town ui-ready crystal state: %v", err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("town ui-ready replay repeated: %x", connection.write.Bytes())
	}

	// Character 21 then picks white; the switch must not have rewritten or leaked
	// character 19's durable value.
	doc075SelectCrystal(t, service, session, 1)
	if got := doc075StoredSelection(t, repositories, "19"); got != 0 {
		t.Fatalf("stored selection for character 19 = %d, want 0 after the switch", got)
	}
	if got := doc075StoredSelection(t, repositories, "21"); got != 1 {
		t.Fatalf("stored selection for character 21 = %d, want 1", got)
	}

	// Switching back to character 19 replays its own black selection again.
	if err := service.handleUpperReturnSelectCharacter(session, nil); err != nil {
		t.Fatalf("op7 return select back: %v", err)
	}
	connection.write.Reset()
	service.bindGameSessionCharacter(session, 19)
	service.armCurrentInitialTownRoute(session, 19)
	if err := service.sendCurrentCrystalContractTownUIReadyState(session); err != nil {
		t.Fatalf("switch-back town ui-ready crystal state: %v", err)
	}
	back, trailingBack := nextCrystalPacket(t, connection.write.Bytes())
	assertCurrentCrystalStatePacket(t, back, []byte{0, 0})
	if len(trailingBack) != 0 {
		t.Fatalf("switch-back op898 state trailing=%x", trailingBack)
	}
}
