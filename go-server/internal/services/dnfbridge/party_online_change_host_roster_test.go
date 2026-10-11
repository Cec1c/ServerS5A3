package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/party"
)

// changeHostFixture 造两个同频道、已绑定角色、本地 town 上下文已提交的会话，
// 把它们放进同一个 runtime party（1001 队长），并把双方登记到同一个城镇区域，
// 这样 `broadcastCurrentPartyActorProjection` 的"队友那条 op9"也会真实发出。
// townOwnerChannel 传 0 表示"副本上下文"（成员行走跨频道身份块分支），
// 传频道号表示城镇上下文。
func changeHostFixture(t *testing.T, channelID, townOwnerChannel byte) (*Service, *gameSession, *gameSession, uint64, uint64) {
	t.Helper()
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	service.onlinePlayers = newOnlinePlayerManager()
	channel := channelcatalog.Channel{ID: int(channelID), Type: 11, Name: "ch", Port: 10000}
	leader := &gameSession{
		conn:                            &bufferConn{},
		channel:                         channel,
		selectedCharacterID:             1001,
		townActorOwnerChannel:           townOwnerChannel,
		connectionTownActorOwnerChannel: channelID,
	}
	member := &gameSession{
		conn:                            &bufferConn{},
		channel:                         channel,
		selectedCharacterID:             1002,
		townActorOwnerChannel:           townOwnerChannel,
		connectionTownActorOwnerChannel: channelID,
	}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(member, 1002)
	leaderIdentity, _ := service.boundGameSessionCharacterSnapshot(leader)
	memberIdentity, _ := service.boundGameSessionCharacterSnapshot(member)

	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: leaderIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
		UserID:            1002,
		SessionGeneration: memberIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
	})
	if !joined.OK || len(joined.Party.Members) != 2 {
		t.Fatalf("join runtime party: %+v", joined)
	}
	service.publishRuntimePartySnapshot(joined.Party)
	// 同一城镇区域（同频道、同实例）⇒ 队友 actor 投影按区域在线表广播。
	for _, info := range []*onlinePlayerInfo{
		{CharacterID: 1001, AccountID: "dnf:1001", Name: "leader", Job: 1, Level: 50,
			TownID: 38, AreaID: 2, ChannelID: int(channelID), Instance: 0, Session: leader},
		{CharacterID: 1002, AccountID: "dnf:1002", Name: "member", Job: 1, Level: 50,
			TownID: 38, AreaID: 2, ChannelID: int(channelID), Instance: 0, Session: member},
	} {
		service.onlinePlayers.EnterArea(info)
	}
	return service, leader, member, leaderIdentity.generation, memberIdentity.generation
}

func writeFrames(t *testing.T, session *gameSession) []upperFrame {
	t.Helper()
	conn := session.conn.(*bufferConn)
	frames := splitUpperFrames(t, conn.write.Bytes())
	conn.write.Reset()
	return frames
}

type changeHostFrames struct {
	realtime   int
	realtimeBP []byte
	records    [][]byte
	removals   int
}

func collectChangeHostFrames(t *testing.T, session *gameSession) changeHostFrames {
	t.Helper()
	collected := changeHostFrames{}
	for _, frame := range writeFrames(t, session) {
		switch frame.msgID {
		case 0x0099:
			collected.realtime++
			collected.realtimeBP = frame.body
		case 0x0009:
			if len(frame.body) > 9 && frame.body[9] == currentSceneOp9ActorRemoveKind {
				collected.removals++
				continue
			}
			if len(frame.body) > 9 && frame.body[9] == currentSceneOp9ActorDisplayKind {
				collected.records = append(collected.records, frame.body)
			}
		}
	}
	return collected
}

// assertChangeHostRoster 断言一次"委任队长"之后，某个成员客户端收到的完整队伍表：
//   - **恰好一份** op153，且是带真实 HP 的完整名单（不是 1 字节的空实时名单）；
//   - 每个成员各一条 op9 kind0（自己的 + 队友的），两条的队伍槽位表都指向新队长 0 号位。
func assertChangeHostRoster(t *testing.T, name string, session *gameSession, newLeaderID uint16) {
	t.Helper()
	collected := collectChangeHostFrames(t, session)
	if collected.realtime != 1 {
		t.Fatalf("%s: op153 count=%d want exactly one full realtime table (a 1-byte body clears the whole client slot table)", name, collected.realtime)
	}
	body := collected.realtimeBP
	if len(body) < 11 {
		t.Fatalf("%s: op153 body=% X is an empty realtime list, want member rows with real HP", name, body)
	}
	if body[0] != 2 {
		t.Fatalf("%s: op153 member count=%d want 2 body=% X", name, body[0], body)
	}
	for row := 0; row < 2; row++ {
		offset := 1 + row*5
		uid := uint16(body[offset]) | uint16(body[offset+1])<<8
		if uid == 0 {
			t.Fatalf("%s: op153 row %d has uid 0 body=% X", name, row, body)
		}
		if hp := body[offset+2]; hp != 100 {
			t.Fatalf("%s: op153 row %d uid=%d HP=%d want 100 body=% X", name, row, uid, hp, body)
		}
		if slot := body[offset+4]; slot != byte(row) {
			t.Fatalf("%s: op153 row %d uid=%d slot=%d want %d body=% X", name, row, uid, slot, row, body)
		}
	}
	if len(collected.records) != 2 {
		t.Fatalf("%s: op9 kind0 record count=%d want 2 (own + peer) bodies=% X", name, len(collected.records), collected.records)
	}
	describes := map[uint16]bool{}
	for _, record := range collected.records {
		rows, leaderSlot := parseOp9PartyFrame(t, record, true)
		if len(rows) != 2 {
			t.Fatalf("%s: op9 rows=%d want 2 body=% X", name, len(rows), record)
		}
		if rows[0].uid != newLeaderID || rows[0].slot != 0 {
			t.Fatalf("%s: op9 slot0=%+v want the new leader %d body=% X", name, rows[0], newLeaderID, record)
		}
		if leaderSlot != 0 {
			t.Fatalf("%s: op9 leader slot byte=%d want 0 body=% X", name, leaderSlot, record)
		}
		actorKey := uint16(record[4]) | uint16(record[5])<<8
		if actorKey != 1001 && actorKey != 1002 {
			t.Fatalf("%s: op9 actor key=%d is not a party member body=% X", name, actorKey, record)
		}
		describes[actorKey] = true
	}
	if !describes[1001] || !describes[1002] {
		t.Fatalf("%s: op9 records describe %v, want both 1001 and 1002", name, describes)
	}
}

// 委任队长后必须给**每个成员**重新下发完整队伍表：op153（血条/槽位实时表，带真实
// HP）+ 每个成员一条 op9（自己的 + 队友的）。这是操作者报的"位置前后互换、血量
// 蓝量清空"那条判据的服务端侧断言。
func TestChangePartyHostRebuildsFullRosterForEveryMember(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, currentSceneObjectContext)
	if err := service.sendManagedRuntimePartySnapshots(runtimePartyStateSnapshot(leader)); err != nil {
		t.Fatalf("initial snapshots: %v", err)
	}
	writeFrames(t, leader)
	writeFrames(t, member)

	if err := service.handleOnlineChangePartyHost(leader, []byte{1}); err != nil {
		t.Fatalf("change party host: %v", err)
	}
	assertChangeHostRoster(t, "former-leader", leader, 1002)
	assertChangeHostRoster(t, "new-leader", member, 1002)
}

// 委任队长**不能**再发"整份空快照"（唯一会让客户端清空整张队伍槽位表的包：
// `msg_id=153 body_len=1`）。清档只允许走 op9 kind3 删除记录。
func TestChangePartyHostNeverSendsEmptyPartySnapshot(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	if err := service.sendManagedRuntimePartySnapshots(runtimePartyStateSnapshot(leader)); err != nil {
		t.Fatalf("initial snapshots: %v", err)
	}
	writeFrames(t, leader)
	writeFrames(t, member)

	if err := service.handleOnlineChangePartyHost(leader, []byte{1}); err != nil {
		t.Fatalf("change party host: %v", err)
	}
	for name, session := range map[string]*gameSession{"former-leader": leader, "new-leader": member} {
		collected := collectChangeHostFrames(t, session)
		if collected.realtime != 1 || len(collected.realtimeBP) < 11 {
			t.Fatalf("%s: change-host realtime frames=%d body=% X, want one full table and never the 1-byte empty list",
				name, collected.realtime, collected.realtimeBP)
		}
		if collected.removals == 0 {
			t.Fatalf("%s: change-host cleared the old table with 0 op9 kind3 records", name)
		}
	}
}

// 客户端对同一次操作重发 op121（线上实测：body 同为 `01` 的两条请求）时必须幂等：
// 队伍代数不再翻新，但完整名单仍要重新下发一遍（自愈）。
func TestChangePartyHostRepeatForCurrentLeaderIsIdempotent(t *testing.T) {
	service, leader, member, leaderGeneration, memberGeneration := changeHostFixture(t, 10, 10)
	manager := service.runtimePartyManagerForService()

	// 客户端发的槽位字节就是它界面上那一行：初始名单里 1001 在 0 号位（队长）、
	// 1002 在 1 号位，所以要委任的是槽位 1。转发同一条请求时字节不变。
	const selectedSlot = byte(1)
	if err := service.handleOnlineChangePartyHost(leader, []byte{selectedSlot}); err != nil {
		t.Fatalf("first change party host: %v", err)
	}
	// 新队长在 manager 里已经拿到权威队伍（第一次 transfer 留下的是**同一份**队伍记录）。
	firstTransfer, found := manager.SnapshotByUser(1002, memberGeneration)
	if !found || firstTransfer.Leader != 1002 {
		t.Fatalf("first transfer did not move leadership: found=%t snapshot=%+v", found, firstTransfer)
	}
	// 老队长仍然在这份队伍里（transfer 只是换了队长并把新队长排到 0 号位）：
	// 它自己那份缓存更新过 ⇒ 重发 op121 必须被认成重发，而不是再退役一代。
	if live, liveFound := manager.SnapshotByUser(1001, leaderGeneration); !liveFound || live.ID != firstTransfer.ID {
		t.Fatalf("former leader lost the live party: found=%t snapshot=%+v", liveFound, live)
	}
	writeFrames(t, leader)
	writeFrames(t, member)

	// 重发：线上客户端在界面还没刷新时会把同一个槽位字节再发一次。
	if err := service.handleOnlineChangePartyHost(leader, []byte{selectedSlot}); err != nil {
		t.Fatalf("repeat change party host: %v", err)
	}
	after, found := manager.SnapshotByUser(1002, memberGeneration)
	if !found || after.Leader != 1002 {
		t.Fatalf("repeat changed leadership: found=%t snapshot=%+v", found, after)
	}
	if after.ID != firstTransfer.ID {
		t.Fatalf("repeat retired the generation again: party %d -> %d", firstTransfer.ID, after.ID)
	}
	// 队伍代数没有翻新 ⇒ 客户端不会再被"清档 + 重建"刷一次（位置互换/血量清空的来源）。
	if live, liveFound := manager.SnapshotByUser(1001, leaderGeneration); !liveFound || live.ID != firstTransfer.ID {
		t.Fatalf("former leader left the live party after the repeat: found=%t snapshot=%+v", liveFound, live)
	}
}

// 回城（放弃副本/直接返回城镇）那条路上，会话必须先提交自己的 town actor owner
// 上下文，否则先回城的人拿不到对方的 actor（现场 packet_log.txt:3376
// `game-town-copresence-actor-state-failed … owner channel is not committed`）。
func TestDungeonReturnRebindCommitsTownActorOwnerChannel(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	service.bindGameSessionCharacter(session, session.selectedCharacterID)

	// 模拟进副本后回城：本地 town 上下文仍是副本期的 0，连接频道已提交（19）。
	session.townActorOwnerChannel = currentSceneObjectContext
	session.connectionTownActorOwnerChannel = byte(session.channel.ID)

	service.rebindTownCopresenceAfterDungeonReturn(session, 38, 0, "regression_dungeon_return_owner_commit")

	if got := currentTownActorOwnerContext(session); got != byte(session.channel.ID) {
		t.Fatalf("dungeon return left the town actor owner context uncommitted: got=%d want=%d",
			got, session.channel.ID)
	}
}
