package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func roomClearQuestBridgeFixture(t *testing.T, cleared bool) (*Service, *gameSession, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	svc, rt := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, currentRoomNonBoss: true, singleMonster: true})
	repos, _ := svc.repositoryGroup()
	session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}}
	session.dungeon.runtime = rt
	if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
		t.Fatal(err)
	}
	scene := rt.Session.Snapshot().Scene
	rt.Dungeon.Mazes[rt.MazeIndex].QuestConnection = []int64{0, 3054, -1}
	source := questListTestSource{
		dnfquest.DefaultList: "3146 `parent.qst`\n3054 `child.qst`\n",
		"n_quest/parent.qst": "[type]\n`[quest clear]`\n[int data]\n3054\n",
		"n_quest/child.qst":  fmt.Sprintf("[grade]\n`[sub]`\n[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[clear map]`\n[main quest]\n3146\n[int data]\n%d\n[reward type]\n`[item]`\n[reward int data]\n0 0\n", scene.Map.Map.ID),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	svc.questCatalog, err = dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{
		3146: {Status: "active", ProgressValue: 1}, 3054: {Status: "active", ProgressValue: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if cleared {
		for key := range scene.RuntimeObjects {
			if _, _, err := rt.Room.CommitActorDeathReport(key, rt.Session); err != nil {
				t.Fatal(err)
			}
		}
		if !rt.Session.Snapshot().Scene.Cleared {
			t.Fatal("fixture room not cleared")
		}
	}
	return svc, session, rt, repos
}

func TestRoomClearQuestRequiresCurrentOwnedClearedMaze(t *testing.T) {
	for _, name := range []string{"unfinished", "wrong-character", "stale-runtime", "wrong-maze", "unbound", "wrong-map", "difficulty"} {
		t.Run(name, func(t *testing.T) {
			svc, session, rt, repos := roomClearQuestBridgeFixture(t, name != "unfinished")
			before, _, _ := repos.Quest.Load(context.Background(), "99")
			switch name {
			case "wrong-character":
				session.selectedCharacterID = 100
			case "stale-runtime":
				session.dungeon.runtime = &runtimeDungeonState{}
			case "wrong-maze":
				rt.MazeIndex++
			case "unbound":
				rt.Dungeon.Mazes[rt.MazeIndex].QuestConnection = nil
			case "wrong-map":
				svc.questCatalog = linkedObjectiveBridgeCatalog(t)
			case "difficulty":
				rt.Dungeon.Mazes[rt.MazeIndex].QuestConnection[2] = 4
			}
			if err := svc.persistCurrentDungeonRoomClearQuestLocked(session, rt); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Quest.Load(context.Background(), "99")
			if !reflect.DeepEqual(before, after) || session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatalf("unproven event changed task or notified: %+v", after)
			}
		})
	}
}

type roomClearBridgeFailCommit struct {
	dnfrepo.CharacterSettlementUnitOfWork
	err error
}

func (u roomClearBridgeFailCommit) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return u.err
	})
}

func TestRoomClearQuestRollbackRetryAndSilentReplay(t *testing.T) {
	svc, session, rt, repos := roomClearQuestBridgeFixture(t, true)
	ctx := context.Background()
	before, _, _ := repos.Quest.Load(ctx, "99")
	failure := errors.New("room quest late commit failure")
	broken := repos
	broken.CharacterSettlement = roomClearBridgeFailCommit{repos.CharacterSettlement, failure}
	svc.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	if err := svc.persistCurrentDungeonRoomClearQuestLocked(session, rt); !errors.Is(err, failure) {
		t.Fatalf("failure=%v", err)
	}
	after, _, _ := repos.Quest.Load(ctx, "99")
	if !reflect.DeepEqual(before, after) || session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("failed transaction leaked state or notification")
	}
	key := runtimeDungeonRoomKeyFromScene(rt.Session.Snapshot().Scene)
	receipt := *rt.roomClearQuestReceipts[key]
	svc.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := svc.persistCurrentDungeonRoomClearQuestLocked(session, rt); err != nil {
		t.Fatal(err)
	}
	after, _, _ = repos.Quest.Load(ctx, "99")
	if after.States[3054].Status != "completed" || after.States[3146].ProgressValue != 0 || after.States[3054].Extra["completion_key"] != receipt.Key {
		t.Fatalf("retry failed: %+v", after.States)
	}
	conn := session.conn.(*bufferConn)
	packets := splitAllFinishBridgePackets(t, conn.write.Bytes())
	if got := finishBridgePacketIDs(packets); !reflect.DeepEqual(got, []string{"0/574", "0/356"}) {
		t.Fatalf("refresh=%v", got)
	}
	conn.write.Reset()
	if err := svc.persistCurrentDungeonRoomClearQuestLocked(session, rt); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || rt.bossDieCheckAccepted || rt.clearMapCompletionPhaseAPersisted {
		t.Fatal("replay notified or entered final settlement")
	}
}

type roomClearFailWriteConn struct {
	bufferConn
	err error
}

func (c *roomClearFailWriteConn) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.bufferConn.Write(p)
}

func TestRoomClearQuestNotificationRetryKeepsCommittedReceipt(t *testing.T) {
	svc, session, rt, repos := roomClearQuestBridgeFixture(t, true)
	conn := &roomClearFailWriteConn{err: errors.New("notification failed")}
	session.conn = conn
	if err := svc.persistCurrentDungeonRoomClearQuestLocked(session, rt); err == nil {
		t.Fatal("expected write failure")
	}
	committed, _, _ := repos.Quest.Load(context.Background(), "99")
	if committed.States[3054].Status != "completed" {
		t.Fatal("quest was not committed before notification")
	}
	conn.err = nil
	if err := svc.persistCurrentDungeonRoomClearQuestLocked(session, rt); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Quest.Load(context.Background(), "99")
	if !reflect.DeepEqual(committed, after) {
		t.Fatal("notification retry changed committed receipt")
	}
	if len(splitAllFinishBridgePackets(t, conn.write.Bytes())) != 2 {
		t.Fatal("missing retried snapshots")
	}
}

func TestRoomClearQuestPeerDispatchUsesMemberQuestEligibility(t *testing.T) {
	for _, eligible := range []bool{true, false} {
		t.Run(fmt.Sprint(eligible), func(t *testing.T) {
			svc, peer, rt, repos := roomClearQuestBridgeFixture(t, true)
			svc.bindGameSessionCharacter(peer, peer.selectedCharacterID)
			rt.partyInstance = &dungeonPartyInstance{id: 91}
			if !eligible {
				if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "99"}); err != nil {
					t.Fatal(err)
				}
			}
			svc.dispatchPartyDungeonPeerFinalRoomCompletion(peer, 91, 1)
			q, _, _ := repos.Quest.Load(context.Background(), "99")
			if eligible && q.States[3054].Status != "completed" {
				t.Fatal("eligible peer did not progress")
			}
			if !eligible && len(q.States) > 0 {
				t.Fatal("ineligible peer gained a task")
			}
			if rt.bossDieCheckAccepted || rt.clearMapCompletionPhaseAPersisted {
				t.Fatal("middle room became dungeon completion")
			}
		})
	}
}
