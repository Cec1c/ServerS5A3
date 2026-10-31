package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// A party return must wait for the receiving player's in-flight event, so
// its scene and quest snapshots cannot overtake a committed quest receipt.
func TestCompletedReturnFanoutWaitsForMemberEventLoop(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "current_run"
		if stale {
			name = "replaced_runtime"
		}
		t.Run(name, func(t *testing.T) { testCompletedReturnOwnerQueue(t, stale) })
	}
}

func testCompletedReturnOwnerQueue(t *testing.T, stale bool) {
	fixture := newCompletionReturnPartyFixture(t)
	if fixture.member.events != nil {
		t.Fatal("fixture already owns member session events; this draft expects to start the real loop")
	}

	// 让 fan-out 的安全闸门放行：member 的 run 已通关，并且它已消费完自己的
	// 结算结果屏。两个字段都属于 member.dungeon.mu 保护的运行时状态。
	fixture.member.dungeon.mu.Lock()
	fixture.memberRuntime.settlementClearRewardSent = true
	fixture.member.dungeon.mu.Unlock()
	if err := fixture.memberRuntime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatalf("complete the member run: %v", err)
	}
	recordCompletionReturnRunKeys(t, fixture)

	// 与现有通关回城用例相同：补齐城镇 actor owner，并登记同屏前置。
	for _, session := range []*gameSession{fixture.leader, fixture.member} {
		session.townMu.Lock()
		session.connectionTownActorOwnerChannel = byte(session.channel.ID)
		session.townActorOwnerChannel = byte(session.channel.ID)
		session.townMu.Unlock()
	}
	registerCompletionReturnTownPresence(t, fixture)

	// 清掉夹具准备阶段可能写出的字节。member 的 bufferConn 本身没有锁，
	// 因此测试只在已有 session.wireMu 下读写它，避免 race/并发读写。
	fixture.member.wireMu.Lock()
	fixture.memberConn.write.Reset()
	fixture.member.wireMu.Unlock()

	eventsCtx, cancelEvents := context.WithCancel(context.Background())
	fixture.member.ctx = eventsCtx
	if err := fixture.service.startGameSessionEvents(eventsCtx, fixture.member); err != nil {
		cancelEvents()
		t.Fatalf("start member session events: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	// Cleanup 必须先放行阻塞事件，再停真实事件循环；否则 Stop 会等待当前
	// 事件而超时。释放/停止都只在 Cleanup 或断言后的单次调用中执行。
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		stopTestGameSessionEvents(t, fixture.service, fixture.member)
		cancelEvents()
	})

	// 占据 member 的唯一处理线程。事件已经轮到、但还没返回；后续队列任务
	// 必须等它释放后才有机会发回城报文或摘 runtime。
	if err := fixture.service.postGameSessionCharacterEvent(
		fixture.member,
		"member-event-loop-gate",
		fixture.member.selectedCharacterID,
		fixture.member.characterGeneration,
		func() error {
			close(started)
			<-release
			if stale {
				fixture.member.dungeon.mu.Lock()
				replacement := &runtimeDungeonState{Session: fixture.memberRuntime.Session, partyInstance: fixture.instance}
				fixture.member.dungeon.runtime = replacement
				fixture.member.dungeon.mu.Unlock()
			}
			return nil
		},
	); err != nil {
		t.Fatalf("submit the member loop gate: %v", err)
	}
	waitTestSignal(t, started, "member event loop gate did not start")

	// leader 的完成和回城都在另一个 goroutine 里触发。leaderDone 能在
	// member 事件仍阻塞时返回，是“没有把 member 工作直接跑到 leader goroutine”
	// 的必要条件；随后再检查 member 的字节和 runtime。
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- ownerQueueLeaderCompletionReturn(
			fixture.service,
			fixture.leader,
			fixture.leaderRuntime,
		)
	}()
	select {
	case err := <-leaderDone:
		if err != nil {
			t.Fatalf("leader completed dungeon return: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader return waited on the blocked member event loop")
	}

	stream, memberStillOwnsRuntime, pending := ownerQueueMemberReturnSnapshot(
		fixture.member,
		fixture.memberRuntime,
		fixture.memberConn,
	)
	// The leader's own town presence may notify peers independently. Only
	// the member's route and quest-list replacement belong to this owner.
	for _, frame := range splitUpperFrames(t, stream) {
		t.Logf("packet before member release: msg=%d bytes=%d", frame.msgID, len(frame.body))
		if frame.msgID == currentSceneTransitionMsgID || frame.msgID == currentActiveQuestSnapshotMsgID {
			t.Fatalf("member received route/quest packet %d before its event loop was released", frame.msgID)
		}
	}
	if !memberStillOwnsRuntime {
		t.Fatal("member runtime was detached before its event loop was released")
	}
	if pending {
		t.Fatal("member town-return intent was committed before its event loop was released")
	}

	// 放行门闩事件。真正排队的 member owner 随后处理正常回城。
	releaseOnce.Do(func() { close(release) })
	barrierCtx, cancelBarrier := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelBarrier()
	if err := fixture.service.callGameSession(barrierCtx, fixture.member, "return-queue-barrier", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if stale {
		fixture.member.wireMu.Lock()
		after := append([]byte(nil), fixture.memberConn.write.Bytes()...)
		fixture.member.wireMu.Unlock()
		if len(after) != len(stream) {
			t.Fatal("stale return wrote packets to replacement runtime")
		}
		fixture.member.dungeon.mu.Lock()
		replacement := fixture.member.dungeon.runtime
		fixture.member.dungeon.mu.Unlock()
		if replacement == nil || replacement == fixture.memberRuntime {
			t.Fatal("replacement runtime lost")
		}
		return
	}

	stream, memberStillOwnsRuntime, _ = waitOwnerQueueMemberReturn(
		fixture.member,
		fixture.memberRuntime,
		fixture.memberConn,
		5*time.Second,
	)
	if memberStillOwnsRuntime {
		t.Fatal("member runtime was not detached after its event loop resumed")
	}
	if len(stream) == 0 {
		t.Fatal("member received no town-return packets after its event loop resumed")
	}
	foundTownTransition := false
	for _, frame := range splitUpperFrames(t, stream) {
		if frame.msgID == currentSceneTransitionMsgID {
			foundTownTransition = true
			break
		}
	}
	if !foundTownTransition {
		t.Fatalf("member resumed without the typed town-transition packet (msg_id=%d)", currentSceneTransitionMsgID)
	}
}

// ownerQueueLeaderCompletionReturn 是现有 completeRunAndReturnToTown 的可返回错误版本，
// 供另一个 goroutine 调用；testing.T 的 Fatal 不能从该 goroutine 直接调用。
func ownerQueueLeaderCompletionReturn(
	service *Service,
	leader *gameSession,
	runtime *runtimeDungeonState,
) error {
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		return err
	}
	transition, err := service.prepareCurrentDungeonTownTransitionForSession(
		context.Background(),
		leader,
		leader.selectedCharacterID,
	)
	if err != nil {
		return err
	}
	return service.sendCurrentCompletedDungeonReturnToTown(
		leader,
		runtime,
		transition,
		uint16(dnfenum.CmdPacketEplpCommand),
		"member_loop_owner",
	)
}

// ownerQueueMemberReturnSnapshot 在既有 wireMu 下复制连接字节，再在 dungeon.mu 下
// 读取 runtime 归属。两个锁依次获取并立即释放，不制造新的锁序。
func ownerQueueMemberReturnSnapshot(
	member *gameSession,
	runtime *runtimeDungeonState,
	conn *bufferConn,
) ([]byte, bool, bool) {
	member.wireMu.Lock()
	stream := append([]byte(nil), conn.write.Bytes()...)
	member.wireMu.Unlock()

	member.dungeon.mu.Lock()
	stillOwnsRuntime := member.dungeon.runtime == runtime
	pending := runtime.townReturnPending
	member.dungeon.mu.Unlock()
	return stream, stillOwnsRuntime, pending
}

func waitOwnerQueueMemberReturn(
	member *gameSession,
	runtime *runtimeDungeonState,
	conn *bufferConn,
	timeout time.Duration,
) ([]byte, bool, bool) {
	deadline := time.Now().Add(timeout)
	for {
		stream, stillOwnsRuntime, pending := ownerQueueMemberReturnSnapshot(member, runtime, conn)
		if !stillOwnsRuntime && len(stream) > 0 {
			return stream, false, pending
		}
		if !time.Now().Before(deadline) {
			return stream, stillOwnsRuntime, pending
		}
		time.Sleep(5 * time.Millisecond)
	}
}
