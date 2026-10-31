package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场（2026-09-28 批次 4 第 4 项）：「切角色后能看到之前自己账号的角色还在城镇内，过 1s 后刷新掉」。
//
// 服务器侧根因：城镇存在感按**角色 id** 分桶，而 op7 只清 session.selectedCharacterID，
// 既不摘表也不发 despawn ⇒ 上一个角色还是"同区活着的玩家"：
//
//	① 新角色 EnterArea 把他当同屏队友投影回**同一条连接**（op2 mode0/mode1 + op9 + op23）；
//	② op24 名册多一行（现场 `len=36 / rows=2`，正常 `len=28 / rows=1`）。
//
// 修法：op7 的唯一收口点 resetGameSessionForCharacterSelect 里用 LeaveAreaSession 摘掉
// 上一个角色，并向同区剩下的玩家补发 NOTI 0x0006（= 厂商 A21 在 op7 上的
// `await _townHandler.NotifyLeaveAsync(session)`）。端到端取证见
// `_dist\stale-town-peer-diagnosis.md`。
//
// 本文件钉四件事：
//
//	① 决定用例：切角色（29 → 3）必须把 29 从区域里摘掉 + 给剩下的 17 发一条 op6(uid=29)，
//	   且新角色 op24 名册里**没有** 29 那一行；
//	② 现场同款（城里只有自己上一个角色）：op24 回到 rows=1 / body 12（= 现场 len 28）；
//	③ 回退值（off）：与今天逐字节相同 —— 零发包、29 仍留在表里、op24 仍是 rows=2 / body 20
//	   （= 现场 len 36），且 op7 自己那条连接的字节流与修复档完全一致；
//	④ 普通再进城（同一个角色，没有 op7）不得摘掉任何存在感；且记录属于**别的常住连接**时
//	   （辅助连接保护，LeaveAreaSession 的会话校验）op7 也不得驱逐。

const (
	townSwitchPreviousCharacterID uint16 = 29
	townSwitchNextCharacterID     uint16 = 3
	townSwitchPeerCharacterID     uint16 = 17
	townSwitchTownID              byte   = 38
	townSwitchAreaID              byte   = 0
)

type townSwitchFixture struct {
	service      *Service
	session      *gameSession
	peer         *gameSession
	repositories dnfrepo.Group
}

func saveTownSwitchCharacter(
	t *testing.T,
	repositories dnfrepo.Group,
	characterID string,
	accountID string,
	name string,
	job string,
	positionX int64,
	positionY int64,
) {
	t.Helper()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Name:        name,
		Job:         job,
		Level:       1,
		Stats: map[string]int64{
			"town_id":    int64(townSwitchTownID),
			"area_id":    int64(townSwitchAreaID),
			"pos_x":      positionX,
			"pos_y":      positionY,
			"direction":  5,
			"area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// newTownSwitchFixture 复刻现场：本会话先选中角色 29，29 按角色 id 登记在城镇 (38,0) 的共享桶里，
// 记录里的 Session 就是**本会话**（现场两个方向的帧因此都落同一条连接）；另一个玩家 17 同频道同区。
func newTownSwitchFixture(t *testing.T) townSwitchFixture {
	t.Helper()
	service, session, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	channel := session.channel
	peer := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "town-switch-peer",
		channel:                         channel,
		residentChannel:                 channel,
		selectedCharacterID:             townSwitchPeerCharacterID,
		townSceneReadyCharacterID:       townSwitchPeerCharacterID,
		connectionTownActorOwnerChannel: byte(channel.ID),
		townActorOwnerChannel:           byte(channel.ID),
	}
	service.bindGameSessionCharacter(peer, townSwitchPeerCharacterID)
	service.bindGameSessionCharacter(session, townSwitchPreviousCharacterID)
	saveTownSwitchCharacter(t, repositories, "17", "dnf:2", "peer", "1", 400, 200)
	saveTownSwitchCharacter(t, repositories, "3", "dnf:1", "next", "0", 900, 250)
	for _, info := range []*onlinePlayerInfo{
		{CharacterID: townSwitchPreviousCharacterID, AccountID: "dnf:1", Name: "previous",
			TownID: townSwitchTownID, AreaID: townSwitchAreaID, PositionX: 450, PositionY: 234,
			Direction: 5, AreaState: 3, Session: session},
		{CharacterID: townSwitchPeerCharacterID, AccountID: "dnf:2", Name: "peer",
			TownID: townSwitchTownID, AreaID: townSwitchAreaID, PositionX: 400, PositionY: 200,
			Direction: 5, AreaState: 3, Session: peer},
	} {
		service.onlinePlayers.EnterArea(info)
	}
	session.conn.(*bufferConn).write.Reset()
	peer.conn.(*bufferConn).write.Reset()
	return townSwitchFixture{service: service, session: session, peer: peer, repositories: repositories}
}

// switchCharacter 走操作者真实路径：op7（客户端切角色的唯一入口）。
func (f townSwitchFixture) switchCharacter(t *testing.T) {
	t.Helper()
	if err := f.service.handleUpperReturnSelectCharacter(f.session, nil); err != nil {
		t.Fatalf("op7 return select: %v", err)
	}
	if f.session.selectedCharacterID != 0 {
		t.Fatalf("op7 selected character = %d, want 0", f.session.selectedCharacterID)
	}
}

// enterTownAsNextCharacter 让新角色进城（op36），返回该连接这一次的全部出站帧。
func (f townSwitchFixture) enterTownAsNextCharacter(t *testing.T) []upperFrame {
	t.Helper()
	f.service.bindGameSessionCharacter(f.session, townSwitchNextCharacterID)
	f.session.townSceneReadyCharacterID = townSwitchNextCharacterID
	f.session.conn.(*bufferConn).write.Reset()
	if err := f.service.handleTownSetUserArea(
		f.session,
		buildTownMoveRequest(townSwitchTownID, townSwitchAreaID, 900, 250, 5),
	); err != nil {
		t.Fatalf("op36 as the next character: %v", err)
	}
	return writeFrames(t, f.session)
}

func townSwitchTransitionFrame(t *testing.T, frames []upperFrame) upperFrame {
	t.Helper()
	for _, frame := range frames {
		if frame.msgID == currentSceneTransitionMsgID {
			return frame
		}
	}
	t.Fatalf("no op24 transition frame in %v", frameIDs(frames))
	return upperFrame{}
}

// parseTownSwitchTransitionRows 按 buildCurrentSceneTransitionBody 的布局（town u8 + area u8 +
// rows u16 + rows × (key u16, x u16, y u16, direction u8, state u8, ignored u8)）切出 op24 名册行。
// 当前24.9原生读取器每行9字节：rows=1 ⇒ body13，rows=2 ⇒ body22。
func parseTownSwitchTransitionRows(t *testing.T, body []byte) []currentSceneTransitionRow {
	t.Helper()
	if len(body) < 4 {
		t.Fatalf("op24 body too short: %d bytes", len(body))
	}
	count := int(binary.LittleEndian.Uint16(body[2:4]))
	if len(body) != 4+9*count {
		t.Fatalf("op24 body len=%d row_count=%d want %d", len(body), count, 4+9*count)
	}
	rows := make([]currentSceneTransitionRow, 0, count)
	for index := 0; index < count; index++ {
		row := body[4+9*index:]
		if row[8] != 1 {
			t.Fatalf("op24 native row trailer=%d want 1", row[8])
		}
		rows = append(rows, currentSceneTransitionRow{
			ObjectOrResourceKey: binary.LittleEndian.Uint16(row[0:2]),
			Value1:              binary.LittleEndian.Uint16(row[2:4]),
			Value2:              binary.LittleEndian.Uint16(row[4:6]),
			Value3:              row[6],
			Value4:              row[7],
		})
	}
	return rows
}

func townSwitchRosterContainsKey(rows []currentSceneTransitionRow, characterID uint16) bool {
	key := currentSceneActorObjectKey(characterID)
	for i := range rows {
		if rows[i].ObjectOrResourceKey == key {
			return true
		}
	}
	return false
}

// 决定用例：29 → 3 的切角色必须把 29 摘出区域、给剩下的 17 发 op6(uid=29)，
// 并且新角色 3 的 op24 名册里不再有 29。
func TestReturnSelectCharacterSelectEvictsPreviousTownPeer(t *testing.T) {
	fixture := newTownSwitchFixture(t)
	previousInfo, ok := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID)
	if !ok {
		t.Fatal("previous character presence missing before select")
	}
	startTownPeerTest(t, fixture.service, fixture.session, &previousInfo)
	peerInfo, ok := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPeerCharacterID)
	if !ok {
		t.Fatal("town peer presence missing before select")
	}
	startTownPeerTest(t, fixture.service, fixture.peer, &peerInfo)
	fixture.switchCharacter(t)
	drainTownPeerTest(t, fixture.service, fixture.session)
	drainTownPeerTest(t, fixture.service, fixture.peer)

	// ① 上一个角色必须已经不在城镇在线表里。
	if player, still := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID); still {
		t.Fatalf("previous character still registered after op7: %+v", player)
	}
	peers := fixture.service.onlinePlayers.GetAreaPlayers(townSwitchTownID, townSwitchAreaID, int(fixture.session.channel.ID))
	if len(peers) != 1 || peers[0].CharacterID != townSwitchPeerCharacterID {
		t.Fatalf("area roster after op7=%+v want only the remaining peer %d", peers, townSwitchPeerCharacterID)
	}

	// ② 同区剩下的玩家收到 NOTI 0x0006：body = u16 小端角色 id（fixed16 头 ⇒ body[3:] 才是载荷）。
	peerFrames := writeFrames(t, fixture.peer)
	if len(peerFrames) != 1 || peerFrames[0].msgID != currentTownUserLeaveMsgID {
		t.Fatalf("remaining peer frames=%v want exactly one op6=%d for char %d",
			frameIDs(peerFrames), currentTownUserLeaveMsgID, townSwitchPreviousCharacterID)
	}
	if !bytes.Equal(peerFrames[0].body[3:], buildTownUserLeaveBody(townSwitchPreviousCharacterID)) {
		t.Fatalf("op6 body=% X want uid body=% X", peerFrames[0].body, buildTownUserLeaveBody(townSwitchPreviousCharacterID))
	}

	// ③ 切换者自己那条连接只多这一个 op7 ack（残留记录的 Session 就是本会话：广播绝不打回自己）。
	localFrames := writeFrames(t, fixture.session)
	if len(localFrames) != 1 || localFrames[0].msgID != uint16(dnfenum.CmdPacketReturnSelectCharacter) {
		t.Fatalf("op7 connection frames=%v want exactly one op7 ack", frameIDs(localFrames))
	}

	// ④ 新角色进城：op24 名册里只有剩下的队友 17 + 自己，**没有**残留的 29。
	rows := parseTownSwitchTransitionRows(t, townSwitchTransitionFrame(t, fixture.enterTownAsNextCharacter(t)).body)
	if len(rows) != 2 || rows[0].ObjectOrResourceKey != currentSceneActorObjectKey(townSwitchPeerCharacterID) ||
		rows[1].ObjectOrResourceKey != currentSceneActorObjectKey(townSwitchNextCharacterID) {
		t.Fatalf("op24 roster=%+v want [peer %d, self %d]", rows, townSwitchPeerCharacterID, townSwitchNextCharacterID)
	}
	if townSwitchRosterContainsKey(rows, townSwitchPreviousCharacterID) {
		t.Fatalf("op24 roster still contains the previous character %d: %+v", townSwitchPreviousCharacterID, rows)
	}
}

// 现场同款：切角色那一刻城里没有**别人**（日志里 same_area_peers 长期为 0），被污染的只有
// 操作者自己的上一个角色。此时 op24 必须回到 rows=1 / body 12 —— 现场对应 `len=28`（修前 `len=36`）。
func TestReturnSelectCharacterSelectOp24RosterDropsPhantomPeer(t *testing.T) {
	fixture := newTownSwitchFixture(t)
	fixture.service.onlinePlayers.LeaveArea(townSwitchPeerCharacterID)
	fixture.switchCharacter(t)
	if player, still := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID); still {
		t.Fatalf("previous character still registered after op7: %+v", player)
	}

	transition := townSwitchTransitionFrame(t, fixture.enterTownAsNextCharacter(t))
	rows := parseTownSwitchTransitionRows(t, transition.body)
	if len(rows) != 1 || rows[0].ObjectOrResourceKey != currentSceneActorObjectKey(townSwitchNextCharacterID) {
		t.Fatalf("op24 roster=%+v want only the new character %d", rows, townSwitchNextCharacterID)
	}
	if len(transition.body) != 13 {
		t.Fatalf("op24 body_len=%d rows=%d want 13 (rows=1)", len(transition.body), len(rows))
	}
}

// 回退值：off/0/false/no/legacy ⇒ 今天的缺陷指纹逐字节保留（零发包、残留角色仍在表里、
// op24 仍是 rows=2 / body 20 = 现场 len=36）。
func TestReturnSelectCharacterSelectRollbackKeepsLegacyStalePeer(t *testing.T) {
	t.Setenv(townCopresenceEvictOnCharacterSelectEnv, "off")
	fixture := newTownSwitchFixture(t)
	fixture.switchCharacter(t)
	// ④（先取证）op7 自己那条连接的字节流：下面 op36 会把这条缓冲区重置，所以先抄一份。
	legacyStream := append([]byte(nil), fixture.session.conn.(*bufferConn).write.Bytes()...)

	// ① 回退档一个字节都不发（改动前 op7 对同区玩家零发包）。
	if frames := writeFrames(t, fixture.peer); len(frames) != 0 {
		t.Fatalf("rollback op7 wrote %v to the remaining peer, want zero bytes", frameIDs(frames))
	}
	// ② 上一个角色照旧留在城镇在线表里，且与新角色同区（这就是现场那条被投影的残留记录）。
	player, still := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID)
	if !still || player.Session != fixture.session {
		t.Fatalf("rollback evicted the previous character: %+v still=%t", player, still)
	}
	if !fixture.service.onlinePlayers.PeerInSameArea(townSwitchPreviousCharacterID, townSwitchPeerCharacterID) {
		t.Fatal("rollback fixture must keep the previous character in the shared area")
	}
	// ③ 新角色 op24 仍被污染：残留的 29 仍在名册里（同区还有队友 17，所以是 3 行）。
	transition := townSwitchTransitionFrame(t, fixture.enterTownAsNextCharacter(t))
	rows := parseTownSwitchTransitionRows(t, transition.body)
	if !townSwitchRosterContainsKey(rows, townSwitchPreviousCharacterID) {
		t.Fatalf("rollback roster=%+v must still contain the stale character %d", rows, townSwitchPreviousCharacterID)
	}
	if len(rows) != 3 || len(transition.body) != 31 {
		t.Fatalf("rollback op24 rows=%d body_len=%d want 3/31 (stale 29 + peer 17 + self 3)",
			len(rows), len(transition.body))
	}
	// ③b 现场同款（城里没有别人）：回退档的 op24 就是被污染的那一版 —— rows=2 / body 20
	//（客户端 packet_len=36），与修复档的 rows=1 / body 12（= 现场 len=28）正好差一个残留行。
	t.Setenv(townCopresenceEvictOnCharacterSelectEnv, "off")
	alone := newTownSwitchFixture(t)
	alone.service.onlinePlayers.LeaveArea(townSwitchPeerCharacterID)
	alone.switchCharacter(t)
	aloneTransition := townSwitchTransitionFrame(t, alone.enterTownAsNextCharacter(t))
	aloneRows := parseTownSwitchTransitionRows(t, aloneTransition.body)
	if len(aloneRows) != 2 || !townSwitchRosterContainsKey(aloneRows, townSwitchPreviousCharacterID) {
		t.Fatalf("rollback roster without other peers=%+v want the stale character %d + self",
			aloneRows, townSwitchPreviousCharacterID)
	}
	if len(aloneTransition.body) != 22 {
		t.Fatalf("rollback op24 body_len=%d rows=%d want 22 (rows=2)",
			len(aloneTransition.body), len(aloneRows))
	}

	// ④ 开关只多"摘表 + 补发 op6"：op7 自己那条连接的字节流两档逐字节相同。
	t.Setenv(townCopresenceEvictOnCharacterSelectEnv, "on")
	enabled := newTownSwitchFixture(t)
	enabled.switchCharacter(t)
	enabledStream := enabled.session.conn.(*bufferConn).write.Bytes()
	if !bytes.Equal(legacyStream, enabledStream) {
		t.Fatalf("op7 connection stream differs between rollback and fixed: legacy=% X fixed=% X",
			legacyStream, enabledStream)
	}
	if _, still := enabled.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID); still {
		t.Fatal("fixed run must evict the previous character")
	}
}

// 普通再进城（同一个角色，没有 op7）不得摘掉任何存在感：换角色档的开关只能挂在 op7 收口点上。
//
// 注：op36 自身会按既有语义给老区域的**路人**发一条 op6 区域变更帧（online_player_manager.go
// 的 broadcastTownPlayerAreaChange），那是改动前就有的行为、与本修法无关，所以这里的判据是
// 存在感状态本身（记录仍在、仍在同一区域、坐标已更新），不是"op6 不存在"。
func TestReturnSelectCharacterSelectOrdinaryReentryKeepsPresence(t *testing.T) {
	fixture := newTownSwitchFixture(t)
	if err := fixture.service.handleTownSetUserArea(
		fixture.session,
		buildTownMoveRequest(townSwitchTownID, townSwitchAreaID, 900, 250, 5),
	); err != nil {
		t.Fatalf("ordinary op36 re-entry: %v", err)
	}
	player, still := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID)
	if !still || player.TownID != townSwitchTownID || player.AreaID != townSwitchAreaID {
		t.Fatalf("ordinary re-entry dropped the presence: %+v still=%t", player, still)
	}
	if player.PositionX != 900 || player.PositionY != 250 {
		t.Fatalf("ordinary re-entry did not re-register the new position: %+v", player)
	}
	if !fixture.service.onlinePlayers.PeerInSameArea(townSwitchPreviousCharacterID, townSwitchPeerCharacterID) {
		t.Fatal("ordinary re-entry must keep both players in the shared area")
	}
}

// 辅助连接保护：记录里的 Session 不是本会话时（同角色短命辅助连接）op7 不得驱逐常住城镇记录，
// 也不得给同区玩家发 op6 —— 这正是选用 LeaveAreaSession 而不是 LeaveArea 的原因。
func TestReturnSelectCharacterSelectKeepsResidentRecordForAuxiliarySession(t *testing.T) {
	fixture := newTownSwitchFixture(t)
	resident := &gameSession{
		conn:                &bufferConn{},
		connID:              "town-switch-resident",
		channel:             fixture.session.channel,
		residentChannel:     fixture.session.channel,
		selectedCharacterID: townSwitchPreviousCharacterID,
	}
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: townSwitchPreviousCharacterID, AccountID: "dnf:1", Name: "previous",
		TownID: townSwitchTownID, AreaID: townSwitchAreaID, PositionX: 450, PositionY: 234,
		Direction: 5, AreaState: 3, Session: resident,
	})
	fixture.switchCharacter(t)

	player, still := fixture.service.onlinePlayers.PlayerForCharacter(townSwitchPreviousCharacterID)
	if !still || player.Session != resident {
		t.Fatalf("op7 on an auxiliary connection evicted the resident record: %+v still=%t", player, still)
	}
	if frames := writeFrames(t, fixture.peer); len(frames) != 0 {
		t.Fatalf("op7 on an auxiliary connection wrote %v to the remaining peer, want zero bytes", frameIDs(frames))
	}
}
