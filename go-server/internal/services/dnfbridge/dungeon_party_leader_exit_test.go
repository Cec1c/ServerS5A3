package dnfbridge

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件钉住缺陷 A（操作者：「队长退出副本 队伍没有解散。正常情况下队伍应该解散且队员还留在图内。
// 现在的情况下队员看队长定住不动，实际上队长已经返回城镇了。」）。
//
// 修法与口径见 dungeon_party_leader_exit.go。这里只断言**线上能看见的东西**：
//   - 队伍真的解散了（中央管理器里查不到这支队伍，每个成员都被摘掉）；
//   - 留图队员的 `session.dungeon.runtime` / `runtime.partyInstance` **一个都没动**（人还在图内）；
//   - 留图队员收到了"队长 actor 已不在"的 op9 kind3 移除帧（object key = 队长角色 id）
//     以及空名册帧（op153 空实时表 + op9 kind3）；
//   - 事件 `game-party-dungeon-leader-exit-disband` / `...-peer-actor-removal` 带齐
//     party_id / leader / 留图成员 / 实例号，供下次实测单看 packet_log 判读。

type leaderExitFixtureState struct {
	service        *Service
	leader         *gameSession
	member         *gameSession
	leaderRuntime  *runtimeDungeonState
	memberRuntime  *runtimeDungeonState
	instance       *dungeonPartyInstance
	logger         *packetLogger
	logPath        string
	leaderConn     *bufferConn
	memberConn     *bufferConn
	instanceIDText string
	partyIDText    string
}

func newLeaderExitFixture(t *testing.T) leaderExitFixtureState {
	t.Helper()
	// Register the authoritative party before dungeon admission, matching the
	// production order. Its allocated ID must also own the shared instance.
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t, func(service *Service, leader, _ *gameSession) {
		if _, ok := service.bootstrapManagedRuntimePartyForSession(leader); !ok {
			t.Fatal("pre-entry party bootstrap failed")
		}
	})
	// 把队伍真正登记进中央管理器（现场口径：管理器持有队伍，客户端投影只是缓存）。
	// 夹具的会话投影已经是 2 人（99 = 队长 / 100 = 队员）。
	if snapshot, ok := service.bootstrapManagedRuntimePartyForSession(leader); !ok || len(snapshot.Members) != 2 {
		t.Fatalf("bootstrap leader party snapshot=%+v ok=%t want 2 members", snapshot, ok)
	}
	if _, found := service.runtimePartyManagerForService().SnapshotByUser(100, 0); !found {
		t.Fatal("bootstrap did not register the second member in the central party manager")
	}
	managed, found := service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found {
		t.Fatal("bootstrap did not register the leader in the central party manager")
	}
	partyIDText := strconv.FormatUint(uint64(managed.ID), 10)
	instance := leaderRuntime.partyInstance
	if instance == nil || instance != memberRuntime.partyInstance {
		t.Fatalf("fixture must share one party dungeon instance: leader=%p member=%p",
			instance, memberRuntime.partyInstance)
	}
	logPath := filepath.Join(t.TempDir(), "leader-exit.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	service.packetLog = logger
	leaderConn := leader.conn.(*bufferConn)
	memberConn := member.conn.(*bufferConn)
	leaderConn.write.Reset()
	memberConn.write.Reset()
	return leaderExitFixtureState{
		service:        service,
		leader:         leader,
		member:         member,
		leaderRuntime:  leaderRuntime,
		memberRuntime:  memberRuntime,
		instance:       instance,
		logger:         logger,
		logPath:        logPath,
		leaderConn:     leaderConn,
		memberConn:     memberConn,
		instanceIDText: strconv.FormatUint(instance.id, 10),
		partyIDText:    partyIDText,
	}
}

// readLeaderExitLog 必须先把日志刷盘/关闭再读：packetLogger 是 64 KiB 缓冲 + 2 秒定时刷新，
// 直接读文件会读到空内容（既有开关用例同样先 Close 再读）。
func (state leaderExitFixtureState) readLog(t *testing.T) string {
	t.Helper()
	if err := state.logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(state.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

// op9 kind3 actor-removal 帧：body = u16 1 | u16 9999 | u16 objectKey | 0 | owner | 0 | kind(3)。
func assertOp9Kind3RemovalFor(frames []upperFrame, objectKey uint16) bool {
	for _, frame := range frames {
		if frame.msgID != uint16(dnfenum.CmdPacketRecoverStamina) || len(frame.body) != 10 {
			continue
		}
		if frame.body[9] != currentSceneOp9ActorRemoveKind {
			continue
		}
		if binary.LittleEndian.Uint16(frame.body[4:6]) == objectKey {
			return true
		}
	}
	return false
}

func TestDungeonLeaderExitDisbandsPartyKeepsMemberInInstanceAndRemovesPeerActor(t *testing.T) {
	fixture := newLeaderExitFixture(t)
	sent := fixture.service.disbandRuntimePartyOnDungeonLeaderExit(
		fixture.leader,
		fixture.instance,
		99,
		"test_leader_exit",
	)
	if sent != 1 {
		t.Fatalf("peer actor removal delivered=%d want 1 (only the member still in the dungeon)", sent)
	}

	// 1) 队伍解散：中央管理器里两个成员都不在了，两支会话的客户端投影也清了。
	manager := fixture.service.runtimePartyManagerForService()
	for _, characterID := range []uint16{99, 100} {
		if snapshot, found := manager.SnapshotByUser(characterID, 0); found {
			t.Fatalf("char %d is still in a managed party after the leader left the dungeon: %+v",
				characterID, snapshot)
		}
	}
	if state := runtimePartyStateSnapshot(fixture.leader); state.PartyID != 0 {
		t.Fatalf("leader client projection still shows party %d", state.PartyID)
	}
	if state := runtimePartyStateSnapshot(fixture.member); state.PartyID != 0 {
		t.Fatalf("member client projection still shows party %d after the disband", state.PartyID)
	}

	// 2) 队员仍然留在本场副本里（运行时与实例指针一个都没动）。
	if fixture.member.dungeon.runtime != fixture.memberRuntime {
		t.Fatal("member lost its dungeon runtime: the member must stay inside the instance")
	}
	if fixture.memberRuntime.partyInstance != fixture.instance {
		t.Fatalf("member was evicted from the shared instance: instance=%p want %p",
			fixture.memberRuntime.partyInstance, fixture.instance)
	}

	// 3) 队员收到了"队长 actor 已不在"的 op9 kind3 移除帧（object key = 队长角色 id = 99）。
	memberFrames := splitUpperFrames(t, fixture.memberConn.write.Bytes())
	if !assertOp9Kind3RemovalFor(memberFrames, 99) {
		t.Fatalf("member never received an op9 kind3 removal for the leader actor: frames=%+v", memberFrames)
	}
	// 名册也清了：空 op153 实时表 + 至少一条 kind3 队伍帧。
	emptyRealtime := false
	rosterRemoval := false
	for _, frame := range memberFrames {
		if frame.msgID == 0x0099 && string(frame.body) == string(party.BuildEmptyRealtimeInfo()) {
			emptyRealtime = true
		}
		if frame.msgID == uint16(dnfenum.CmdPacketRecoverStamina) && len(frame.body) == 10 &&
			frame.body[9] == currentSceneOp9ActorRemoveKind {
			rosterRemoval = true
		}
	}
	if !emptyRealtime || !rosterRemoval {
		t.Fatalf("member roster was not cleared: empty_op153=%t kind3_frame=%t frames=%+v",
			emptyRealtime, rosterRemoval, memberFrames)
	}
	// 队长（已在城镇）也必须收到空名册，否则城镇那边队伍框还挂着已经留在图内的队员。
	if leaderFrames := splitUpperFrames(t, fixture.leaderConn.write.Bytes()); len(leaderFrames) == 0 {
		t.Fatal("the leaving leader received nothing: its town party window would keep the member")
	}

	text := fixture.readLog(t)
	for _, field := range []string{
		"kind=game-party-dungeon-leader-exit-disband",
		"party_id=" + fixture.partyIDText,
		"instance_id=" + fixture.instanceIDText,
		"leader_char_id=99",
		"leaving_char_id=99",
		"member_count_before=2",
		"removed_char_ids=[99 100]",
		"disbanded=true",
		"members_still_in_dungeon=[100]",
		"peer_actor_removal_sent_to=[100]",
		"roster_cleared_sent_to=",
		"leader_object_key=99",
		"member_instance_retained=true",
		"mode=disband",
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("disband event is missing %q\n%s", field, text)
		}
	}
	if !strings.Contains(text, "kind=game-party-dungeon-leader-exit-peer-actor-removal") ||
		!strings.Contains(text, "reason=leader_left_the_dungeon_member_keeps_its_instance_so_the_frozen_peer_actor_must_be_removed") {
		t.Fatalf("peer actor removal event is missing\n%s", text)
	}
}

// 回退档逐字节回到改动前：不碰管理器、一个字节都不发、也不记新事件。
func TestDungeonLeaderExitLegacyLiteralKeepsTodaysBehaviour(t *testing.T) {
	t.Setenv(dungeonPartyLeaderExitEnv, dungeonPartyLeaderExitValueLegacy)
	fixture := newLeaderExitFixture(t)
	if sent := fixture.service.disbandRuntimePartyOnDungeonLeaderExit(
		fixture.leader,
		fixture.instance,
		99,
		"test_leader_exit_legacy",
	); sent != 0 {
		t.Fatalf("legacy mode delivered=%d want 0 packets", sent)
	}
	for name, conn := range map[string]*bufferConn{"member": fixture.memberConn, "leader": fixture.leaderConn} {
		if conn.write.Len() != 0 {
			t.Fatalf("legacy mode wrote bytes to the %s connection: %x", name, conn.write.Bytes())
		}
	}
	if _, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(99, 0); !found {
		t.Fatal("legacy mode mutated the central party manager")
	}
	if state := runtimePartyStateSnapshot(fixture.member); state.PartyID != 12 && strconv.Itoa(state.PartyID) != fixture.partyIDText {
		t.Fatalf("legacy mode cleared the member projection: party_id=%d", state.PartyID)
	}
	if state := runtimePartyStateSnapshot(fixture.leader); state.PartyID != 12 && strconv.Itoa(state.PartyID) != fixture.partyIDText {
		t.Fatalf("legacy mode cleared the leader projection: party_id=%d", state.PartyID)
	}
	if fixture.memberRuntime.partyInstance != fixture.instance ||
		fixture.member.dungeon.runtime != fixture.memberRuntime {
		t.Fatal("legacy mode changed the member runtime")
	}
	text := fixture.readLog(t)
	if strings.Contains(text, "kind=game-party-dungeon-leader-exit") {
		t.Fatalf("legacy mode must not log the new events\n%s", text)
	}
}

// 只有**队长**离场才解散；队员自己回城不触发（操作者口径只针对队长退出副本）。
func TestDungeonMemberExitDoesNotDisbandTheParty(t *testing.T) {
	fixture := newLeaderExitFixture(t)
	// leavingCharacterID=100（队员）而名单里的队长是 99 ⇒ 不触发。
	if sent := fixture.service.disbandRuntimePartyOnDungeonLeaderExit(
		fixture.member,
		fixture.instance,
		100,
		"test_member_exit",
	); sent != 0 {
		t.Fatalf("member exit delivered=%d want 0", sent)
	}
	if _, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(99, 0); !found {
		t.Fatal("a member leaving the dungeon disbanded the party")
	}
	if fixture.memberConn.write.Len() != 0 || fixture.leaderConn.write.Len() != 0 {
		t.Fatalf("member exit wrote bytes: member=%x leader=%x",
			fixture.memberConn.write.Bytes(), fixture.leaderConn.write.Bytes())
	}
	if fixture.memberRuntime.partyInstance != fixture.instance {
		t.Fatal("member exit changed the shared instance")
	}
	if text := fixture.readLog(t); strings.Contains(text, "kind=game-party-dungeon-leader-exit") {
		t.Fatalf("member exit logged a leader-exit event\n%s", text)
	}
}

// 没有队友留在本场实例时（例如全队一起回城）不改变今天的任何行为。
func TestDungeonLeaderExitWithoutMemberInInstanceIsANoop(t *testing.T) {
	fixture := newLeaderExitFixture(t)
	// 模拟队员已经出图：从实例里摘掉它（既有收口点），留图成员集合因此为空。
	fixture.service.releasePartyDungeonInstance(fixture.memberRuntime, 100, "test_member_already_left")
	if sent := fixture.service.disbandRuntimePartyOnDungeonLeaderExit(
		fixture.leader,
		fixture.instance,
		99,
		"test_leader_exit_alone",
	); sent != 0 {
		t.Fatalf("delivered=%d want 0 when no member is left in the instance", sent)
	}
	if fixture.leaderConn.write.Len() != 0 || fixture.memberConn.write.Len() != 0 {
		t.Fatalf("noop path wrote bytes: leader=%x member=%x",
			fixture.leaderConn.write.Bytes(), fixture.memberConn.write.Bytes())
	}
	if _, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(99, 0); !found {
		t.Fatal("noop path mutated the central party manager")
	}
	if text := fixture.readLog(t); strings.Contains(text, "kind=game-party-dungeon-leader-exit") {
		t.Fatalf("noop path logged a leader-exit event\n%s", text)
	}
}

// 走**真实入口**：队长那份 op24 被城镇侧请求确认之后，`commitPendingDungeonReturnForSceneRequest`
// 必须自己触发解散（现场 source 就是这条 `current_exe_op35_position_after_pending_town_return`，
// packet_log.txt:96603 的 `source=back_to_village_committed` 同源）。这条用例防的是
// "修好了解散函数但没接到出图路径上"。
func TestDungeonLeaderBackToVillageCommitDisbandsPartyThroughTheRealEntryPoint(t *testing.T) {
	fixture := newLeaderExitFixture(t)
	fixture.leaderRuntime.townReturnPending = true
	fixture.leaderRuntime.townReturnOp24Sent = true

	// 入口自身在锁外可能因为城镇侧 actor 重建缺数据而返回错误（夹具没有真实 PVF/装备），
	// 这里只断言与缺陷 A 有关的三件事：队伍解散、队友留在图内、队友收到 peer 移除信号。
	_ = fixture.service.commitPendingDungeonReturnForSceneRequest(fixture.leader, "test_back_to_village_commit")

	for _, characterID := range []uint16{99, 100} {
		if snapshot, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(characterID, 0); found {
			t.Fatalf("the real dungeon-exit entry point left char %d inside a managed party: %+v",
				characterID, snapshot)
		}
	}
	if fixture.member.dungeon.runtime != fixture.memberRuntime {
		t.Fatal("the member lost its dungeon runtime at the leader's exit commit")
	}
	memberFrames := splitUpperFrames(t, fixture.memberConn.write.Bytes())
	if !assertOp9Kind3RemovalFor(memberFrames, 99) {
		t.Fatalf("the real entry point did not send the leader-actor removal to the member: frames=%+v", memberFrames)
	}
	text := fixture.readLog(t)
	if !strings.Contains(text, "kind=game-party-dungeon-leader-exit-disband") ||
		!strings.Contains(text, "member_count_before=2") {
		t.Fatalf("the real entry point did not log the disband event\n%s", text)
	}
	if !strings.Contains(text, "members_still_in_dungeon=[100]") {
		t.Fatalf("the disband event must record that the member stayed in the instance\n%s", text)
	}
}
