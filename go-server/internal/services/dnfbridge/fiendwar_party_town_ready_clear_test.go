package dnfbridge

import (
	"encoding/binary"
	"testing"
)

// 超时空遭遇战回城时，最后回城的那条会话在提交点仍带着副本 owner 0：
// 退役的 op9 kind3 用 owner 字节挑选"要销毁哪一条场景队伍记录"，owner=0 只能
// 清掉副本记录，客户端城镇上下文（连接频道）里"自己"那条还在，队伍框就残留。
// 这条回归钉住：过场中的成员被标记，并在城镇 owner 落定后补发一条当前上下文
// 的 kind3。
func TestFiendWarRetireReplaysSelfRemovalAfterTownSceneReady(t *testing.T) {
	f := newLeaderExitFixture(t)
	s := f.service
	const townChannel = 10
	leader := f.leader
	leader.channel.ID = townChannel
	leader.connectionTownActorOwnerChannel = townChannel
	leader.dungeon.mu.Lock()
	leader.confirmedDungeonReturnStatePending = true
	leader.townActorOwnerChannel = currentSceneObjectContext
	leader.dungeon.mu.Unlock()

	frozen, found := s.runtimePartyManagerForService().SnapshotByUser(leader.selectedCharacterID, 0)
	if !found {
		t.Fatal("fixture has no managed party for the returning leader")
	}
	s.retireCurrentFiendWarPartySnapshot(leader, frozen, "test_return_run")
	if !leader.fiendWarPartyClearOnTownSceneReady {
		t.Fatal("a member that was still in the return transition was not marked for a post-scene clear")
	}

	// 模拟 ensureCurrentConfirmedDungeonReturnPlayerState：城镇 owner 已落定、
	// 玩家状态已重建，然后补收口。
	leader.townMu.Lock()
	leader.townActorOwnerChannel = townChannel
	leader.townMu.Unlock()
	f.leaderConn.write.Reset()
	s.flushFiendWarPartyClearOnTownSceneReady(leader, "test_town_scene_ready")
	if leader.fiendWarPartyClearOnTownSceneReady {
		t.Fatal("post-scene clear flag survived the replay")
	}

	foundRemoval := false
	for _, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
		if frame.msgID != 9 || len(frame.body) != 10 || frame.body[9] != currentSceneOp9ActorRemoveKind {
			continue
		}
		objectKey := binary.LittleEndian.Uint16(frame.body[4:6])
		if objectKey != currentSceneActorObjectKey(leader.selectedCharacterID) {
			continue
		}
		if frame.body[7] != townChannel {
			t.Fatalf("post-scene removal owner=%d, want town channel %d (body=%x)", frame.body[7], townChannel, frame.body)
		}
		foundRemoval = true
	}
	if !foundRemoval {
		t.Fatalf("no town-context op9 kind3 removal for the local actor was replayed: %x", f.leaderConn.write.Bytes())
	}
}

// 已经回到城镇、owner 已落定的成员不需要补发：退役帧本身就是正确上下文。
func TestFiendWarRetireDoesNotMarkSettledTownMember(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.leader.confirmedDungeonReturnStatePending = false
	f.leader.townActorOwnerChannel = 10
	frozen, found := f.service.runtimePartyManagerForService().SnapshotByUser(f.leader.selectedCharacterID, 0)
	if !found {
		t.Fatal("fixture has no managed party")
	}
	f.service.retireCurrentFiendWarPartySnapshot(f.leader, frozen, "test_settled_return")
	if f.leader.fiendWarPartyClearOnTownSceneReady {
		t.Fatal("a member whose town context was already settled must not get a second clear")
	}
}
