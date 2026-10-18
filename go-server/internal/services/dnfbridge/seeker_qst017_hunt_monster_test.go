package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func qst017Fixture(t *testing.T, count int64) (*Service, *gameSession, dnfrepo.Group, uint32) {
	t.Helper()
	ctx := context.Background()
	source := bridgeDungeonPVF(false)
	for k, v := range source {
		source[k] = strings.ReplaceAll(strings.ReplaceAll(v, "700", "72"), "3001", "56158")
	}
	source["map/dungeon/test/start.map"] += "56158 10 0 200 200 0 0 0 `[fixed]` `[normal]`\n"
	source[dnfquest.DefaultList] = "3687 `qst017.qst`\n"
	source["n_quest/qst017.qst"] = fmt.Sprintf("[name]\n`年轻时的吉赛尔`\n[grade]\n`[epic]`\n[type]\n`[hunt monster]`\n[int data]\n72 -1 56158 %d\n[reward item]\n10164766 1\n", count)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	repos := qst006Repositories(t, ctx)
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3687: {Status: "active", ProgressValue: count}}}); err != nil {
		t.Fatal(err)
	}
	_, svc, session := qst006Bridge(repos, catalog, nil, nil)
	svc.worldMapTable, svc.worldMapResolver, svc.dungeonMonsterTable = table, resolver, monsters
	svc.dungeonChoice = func(int) (int, error) { return 0, nil }
	svc.dungeonSeed = func() (uint32, error) { return 1, nil }
	rt, _, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 72})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = rt
	if _, err = rt.Room.AnnounceAllActors(rt.Session); err != nil {
		t.Fatal(err)
	}
	return svc, session, repos, rt.Room.Snapshot().Monsters[0].ObjectKey
}

func qst017Death(svc *Service, session *gameSession, key uint32) error {
	body := storyProcDeathBody(key)
	binary.LittleEndian.PutUint16(body[4:], uint16(currentSceneActorObjectKey(session.selectedCharacterID)))
	return svc.handleDungeonMonsterDeath(session, body)
}

func qst017State(t *testing.T, repos dnfrepo.Group) dnfrepo.QuestState {
	t.Helper()
	record, _, err := repos.Quest.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	return record.States[3687]
}

func TestSeekerQST017HuntMonsterDeath(t *testing.T) {
	svc, session, repos, key := qst017Fixture(t, 1)
	if err := qst017Death(svc, session, key); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.dungeon.runtime.Room.DefeatedDeathTarget(key); !ok {
		t.Fatal("death not accepted")
	}
	state := qst017State(t, repos)
	if state.ProgressValue != 0 || state.Extra["reward_state"] != "pending" {
		t.Fatalf("accepted death left quest=%+v; want progress 0 and pending", state)
	}
	if err := qst017Death(svc, session, key); err != nil {
		t.Fatal(err)
	}
	if got := qst017State(t, repos); got.ProgressValue != 0 {
		t.Fatalf("duplicate changed progress: %+v", got)
	}
	inv, _, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range inv.Slots {
		if slot.ItemID == 10164766 && slot.Count > 0 {
			t.Fatal("death granted reward before FinishQuest")
		}
	}
}

func TestSeekerQST017HuntMonsterRetries(t *testing.T) {
	for _, failure := range []string{"save", "snapshot", "death"} {
		t.Run(failure, func(t *testing.T) {
			svc, session, repos, key := qst017Fixture(t, 3)
			store := &qst003RepairQuestStore{QuestRepository: repos.Quest, fail: failure == "save"}
			repos.Quest = store
			svc.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			if failure != "save" {
				at := 1
				if failure == "snapshot" {
					at = 2
				}
				session.conn = &failNthDungeonWriteConn{failAt: at, err: errors.New("qst017 injected write failure")}
			}
			if err := qst017Death(svc, session, key); err == nil {
				t.Fatal("expected injected failure")
			}
			want := int64(2)
			if failure == "save" || failure == "death" {
				want = 3
			}
			if got := qst017State(t, repos).ProgressValue; got != want {
				t.Fatalf("after failure progress=%d want=%d", got, want)
			}
			store.fail = false
			session.conn = &bufferConn{}
			for i := 0; i < 2; i++ {
				if err := qst017Death(svc, session, key); err != nil {
					t.Fatal(err)
				}
			}
			if got := qst017State(t, repos).ProgressValue; got != 2 {
				t.Fatalf("replay progress=%d want=2", got)
			}
			other := session.dungeon.runtime.Room.Snapshot().Monsters[1].ObjectKey
			if err := qst017Death(svc, session, other); err != nil {
				t.Fatal(err)
			}
			if got := qst017State(t, repos).ProgressValue; got != 1 {
				t.Fatalf("second monster progress=%d want=1", got)
			}
			if err := qst017Death(svc, session, key); err != nil {
				t.Fatal(err)
			}
			if got := qst017State(t, repos).ProgressValue; got != 1 {
				t.Fatalf("old death after second monster progress=%d", got)
			}
		})
	}
}

func TestSeekerQST017HuntMonsterUnknownObject(t *testing.T) {
	svc, session, repos, _ := qst017Fixture(t, 1)
	if err := qst017Death(svc, session, 65000); err != nil {
		t.Fatal(err)
	}
	if got := qst017State(t, repos).ProgressValue; got != 1 {
		t.Fatalf("unknown object progress=%d", got)
	}
}

type qst017CommittedErrorStore struct {
	dnfrepo.QuestRepository
	fail bool
}

func (s *qst017CommittedErrorStore) SaveFields(ctx context.Context, r dnfrepo.QuestRecord, fields ...dnfrepo.QuestField) error {
	if err := dnfrepo.SaveQuestFields(ctx, s.QuestRepository, r, fields...); err != nil {
		return err
	}
	if s.fail {
		s.fail = false
		return errors.New("qst017 commit succeeded but reply lost")
	}
	return nil
}

func TestSeekerQST017HuntMonsterCommittedErrorRetry(t *testing.T) {
	svc, session, repos, key := qst017Fixture(t, 3)
	repos.Quest = &qst017CommittedErrorStore{QuestRepository: repos.Quest, fail: true}
	svc.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := qst017Death(svc, session, key); err == nil {
		t.Fatal("expected ambiguous commit failure")
	}
	if got := qst017State(t, repos).ProgressValue; got != 2 {
		t.Fatalf("persisted progress=%d", got)
	}
	if err := qst017Death(svc, session, key); err != nil {
		t.Fatal(err)
	}
	if got := qst017State(t, repos).ProgressValue; got != 2 {
		t.Fatalf("retry progress=%d", got)
	}
}

func TestSeekerQST017HuntMonsterScopeControls(t *testing.T) {
	for _, scope := range []string{"wrong_dungeon", "wrong_monster", "inactive"} {
		t.Run(scope, func(t *testing.T) {
			svc, session, repos, key := qst017Fixture(t, 1)
			if scope == "inactive" {
				record, _, err := repos.Quest.Load(context.Background(), "19")
				if err != nil {
					t.Fatal(err)
				}
				state := record.States[3687]
				state.Status = "completed"
				record.States[3687] = state
				if err = repos.Quest.Save(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			} else {
				dungeon, monster := 72, 56158
				if scope == "wrong_dungeon" {
					dungeon = 73
				} else {
					monster = 56159
				}
				source := bridgePVFSource{dnfquest.DefaultList: "3687 `scope.qst`\n", "n_quest/scope.qst": fmt.Sprintf("[type]\n`[hunt monster]`\n[int data]\n%d -1 %d 1\n", dungeon, monster)}
				idx, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
				if err != nil {
					t.Fatal(err)
				}
				svc.questCatalog, err = dnfquest.Load(context.Background(), idx)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := qst017Death(svc, session, key); err != nil {
				t.Fatal(err)
			}
			if got := qst017State(t, repos).ProgressValue; got != 1 {
				t.Fatalf("%s progress=%d", scope, got)
			}
		})
	}
}
