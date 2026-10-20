package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 2026-09-28 夜另外两条修法（第 ① 条见 party_peer_appearance_subtype0_test.go）：
//
//	② `DNFBRIDGE_PARTY_DUNGEON_RUN_KEY_TOWN_RESET`
//	   解散/回城之后失效的副本运行键必须在**城镇上下文**被清掉，否则重新组队时
//	   新队伍会被误判成"还在上一场副本里"，在城镇里收到 owner=0 的副本上下文队伍帧。
//	   现场：packet_log.txt:109133 解散 → :109390 新队伍在城镇里发
//	   `run_key_dungeon_id=310` 的 `game-dungeon-party-frame-roster-send`。
//	③ `DNFBRIDGE_TOWN_COPRESENCE_RESEND_ON_POST_TRANSITION`
//	   城镇过场后状态链（op24/op35 重建）走的是快照版收尾，绕过了唯一的城镇补发调用
//	   （scene_finish_loading.go:69）⇒ 回城之后一次同屏投影都不补。
//	   现场指纹：全日志 `game-town-copresence-actor-resend` = 0（副本孪生那条 = 4）。

// leaveDungeonForTest 把一条会话的副本运行时摘掉，模拟"已经回到城镇"。
func leaveDungeonForTest(session *gameSession) {
	if session == nil {
		return
	}
	session.dungeon.mu.Lock()
	session.dungeon.runtime = nil
	session.dungeon.mu.Unlock()
}

// TestDungeonRunKeyIsResetOnceTheSessionLeavesTheDungeon 钉住缺陷②的第一半：
// 会话没有活跃副本运行时时，城镇上下文收口点必须把那条过期键清掉；真的在副本里
// （有活跃 runtime）则一个字节都不动；回退档不清。
func TestDungeonRunKeyIsResetOnceTheSessionLeavesTheDungeon(t *testing.T) {
	t.Run("fix档 clears the stale key once the runtime is gone", func(t *testing.T) {
		t.Setenv(dungeonPartyRunKeyTownResetEnv, cloneAvatarAppearanceOn)
		service, leader, _, _, _ := partyDungeonDeathFixture(t)
		if _, ready := service.currentDungeonPartyRunKey(leader); !ready {
			t.Fatal("fixture must record a dungeon run key")
		}
		// 仍在副本里：收口点不得动它。
		if service.forgetStaleDungeonPartyRunKeyOutsideDungeon(leader, "test_still_in_dungeon") {
			t.Fatal("a session with an active dungeon runtime must keep its run key")
		}
		if _, ready := service.currentDungeonPartyRunKey(leader); !ready {
			t.Fatal("run key disappeared while the session was still in the dungeon")
		}
		leaveDungeonForTest(leader)
		if !service.forgetStaleDungeonPartyRunKeyOutsideDungeon(leader, "test_back_in_town") {
			t.Fatal("the stale run key must be cleared once the session left the dungeon")
		}
		if _, ready := service.currentDungeonPartyRunKey(leader); ready {
			t.Fatal("run key still valid after the town reset")
		}
		// 幂等：再清一次什么都不发生。
		if service.forgetStaleDungeonPartyRunKeyOutsideDungeon(leader, "test_back_in_town_again") {
			t.Fatal("second reset must be a no-op")
		}
	})

	t.Run("legacy档 keeps today's key", func(t *testing.T) {
		t.Setenv(dungeonPartyRunKeyTownResetEnv, cloneAvatarAppearanceLegacy)
		service, leader, _, _, _ := partyDungeonDeathFixture(t)
		leaveDungeonForTest(leader)
		if service.forgetStaleDungeonPartyRunKeyOutsideDungeon(leader, "test_legacy") {
			t.Fatal("legacy档 must not clear the run key")
		}
		if _, ready := service.currentDungeonPartyRunKey(leader); !ready {
			t.Fatal("legacy档 must keep today's stale key")
		}
	})
}

// TestFreshPartyInTownDoesNotInheritTheDisbandedDungeonRunKey 钉住缺陷②的第二半
// （也就是"解散后重新组队 = 一支从未存在过的队伍"这条验收口径）：
// 两个成员都已回城、但记着上一场的运行键时，
//   - 修复档：`sendDungeonPartyFrameRoster` 一帧都不发（今天的日志里它在城镇里发了 4 条记录）；
//   - 回退档：照旧发出 `run_key_dungeon_id=<上一场>` 的副本上下文队伍帧。
func TestFreshPartyInTownDoesNotInheritTheDisbandedDungeonRunKey(t *testing.T) {
	countFrames := func(t *testing.T, switchValue string) (int, string) {
		t.Helper()
		t.Setenv(dungeonPartyRunKeyTownResetEnv, switchValue)
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower, _, _ := partyDungeonDeathFixture(t)
		leaveDungeonForTest(leader)
		leaveDungeonForTest(follower)
		leader.conn.(*bufferConn).write.Reset()
		service.packetLog = logger
		delivered := service.sendDungeonPartyFrameRoster(runtimePartyStateSnapshot(leader), "test_fresh_party_in_town")
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return delivered, string(logged)
	}

	fixedDelivered, fixedLog := countFrames(t, cloneAvatarAppearanceOn)
	if fixedDelivered != 0 {
		t.Fatalf("fix档 delivered=%d want 0 (a fresh town party must not receive dungeon-context frames)", fixedDelivered)
	}
	for _, want := range []string{
		"kind=game-party-dungeon-run-key-cleared",
		"char_id=99",
		"char_id=100",
		"reason=session_has_no_active_dungeon_runtime_so_the_recorded_run_key_belongs_to_a_disbanded_run",
		"kind=game-dungeon-party-frame-roster-skipped",
		"reason=no_member_has_a_valid_dungeon_run_key_after_the_town_reset",
	} {
		if !strings.Contains(fixedLog, want) {
			t.Fatalf("fix档 packet log missing %q\n%s", want, fixedLog)
		}
	}
	if strings.Contains(fixedLog, "kind=game-dungeon-party-frame-roster-send") {
		t.Fatalf("fix档 must not emit the dungeon-context roster in town\n%s", fixedLog)
	}
	if strings.Contains(fixedLog, "kind=game-party-dungeon-run-key-cleared") == false {
		t.Fatal("fix档 must record the cleared run keys")
	}

	legacyDelivered, legacyLog := countFrames(t, cloneAvatarAppearanceLegacy)
	if legacyDelivered == 0 {
		t.Fatalf("legacy档 delivered=0, want today's dungeon-context roster (live value record_count=4)\n%s", legacyLog)
	}
	if !strings.Contains(legacyLog, "kind=game-dungeon-party-frame-roster-send") {
		t.Fatalf("legacy档 must keep today's roster event\n%s", legacyLog)
	}
	if strings.Contains(legacyLog, "kind=game-party-dungeon-run-key-cleared") {
		t.Fatalf("legacy档 must not log a run-key reset\n%s", legacyLog)
	}
}

// TestTownCopresenceResendRunsAfterEveryTownPostTransition 钉住缺陷③：
// 城镇 post-transition 每完成一代，就必须重新投影一次同屏对端；同一区域、同一批人
// 也不能被幂等键吃掉（这正是"回到同一个区域"的现场）。回退档一个字节都不发。
func TestTownCopresenceResendRunsAfterEveryTownPostTransition(t *testing.T) {
	t.Setenv(currentTownCopresenceResendEnv, cloneAvatarAppearanceOn)

	t.Run("fix档 republishes on every generation", func(t *testing.T) {
		t.Setenv(currentTownCopresencePostTransitionResendEnv, cloneAvatarAppearanceOn)
		fixture := newTownResendFixture(t)
		fixture.localBuf.write.Reset()
		fixture.service.resendTownCopresenceAfterTownPostTransition(fixture.local, "test_post_transition")
		first := fixture.frames()
		if len(first) == 0 {
			t.Fatal("first post-transition generation emitted nothing")
		}
		fixture.localBuf.write.Reset()
		fixture.service.resendTownCopresenceAfterTownPostTransition(fixture.local, "test_post_transition")
		second := fixture.frames()
		if len(second) == 0 {
			t.Fatal("second post-transition generation must republish (the idempotency key must rotate)")
		}
		if len(first) != len(second) {
			t.Fatalf("generation frame counts differ: first=%d second=%d", len(first), len(second))
		}
	})

	t.Run("legacy档 emits nothing", func(t *testing.T) {
		t.Setenv(currentTownCopresencePostTransitionResendEnv, cloneAvatarAppearanceLegacy)
		fixture := newTownResendFixture(t)
		fixture.localBuf.write.Reset()
		fixture.service.resendTownCopresenceAfterTownPostTransition(fixture.local, "test_post_transition_legacy")
		if frames := fixture.frames(); len(frames) != 0 {
			t.Fatalf("legacy档 emitted %d frames, want 0", len(frames))
		}
	})

	t.Run("dungeon context is left to the dungeon resend", func(t *testing.T) {
		t.Setenv(currentTownCopresencePostTransitionResendEnv, cloneAvatarAppearanceOn)
		service, leader, _, _, _ := partyDungeonDeathFixture(t)
		leader.conn.(*bufferConn).write.Reset()
		service.resendTownCopresenceAfterTownPostTransition(leader, "test_post_transition_dungeon")
		if frames := splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes()); len(frames) != 0 {
			t.Fatalf("a session with an active dungeon runtime must be left to the dungeon resend, got %d frames", len(frames))
		}
	})
}

// TestTownPostTransitionWrapperCallsTheResend 钉住接线：对外入口
// `sendCurrentTownPostTransitionPlayerState` 必须在尾段跑完之后调用补发
// （`sendCurrentTownPostTransitionPlayerStateLocked` 是持有 townPostTransition.mu 的实体）。
func TestTownPostTransitionWrapperCallsTheResend(t *testing.T) {
	t.Setenv(currentTownCopresencePostTransitionResendEnv, cloneAvatarAppearanceOn)
	fixture := newTownResendFixture(t)
	fixture.localBuf.write.Reset()
	// 未 arm 的世代 ⇒ 实体链直接返回 nil，但包装层仍然要走一次补发。
	if err := fixture.service.sendCurrentTownPostTransitionPlayerState(fixture.local, "test_wrapper"); err != nil {
		t.Fatalf("wrapper returned %v", err)
	}
	if frames := fixture.frames(); len(frames) == 0 {
		t.Fatal("wrapper did not reach the town co-presence resend")
	}
	_ = dnfrepo.CharacterRecord{}
	_ = context.Background()
}
