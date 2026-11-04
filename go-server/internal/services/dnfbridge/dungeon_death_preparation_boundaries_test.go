package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"testing"
	"time"
)

func TestDeathPreparationRecoveryCannotCrossRoomOrCharacter(t *testing.T) {
	for _, change := range []string{"room", "runtime", "character", "generation", "revoked"} {
		t.Run(change, func(t *testing.T) {
			s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
			p := ps[0]
			q := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = q
			f := &deathPreparationFault{base: repos.QuestMaterialPlan, fail: true}
			repos.QuestMaterialPlan = f
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			if err := s.armQuestMaterialRecovery(p); err != nil {
				t.Fatal(err)
			}
			if err := partyMaterialDeath(s, p, key); err != nil {
				t.Fatal(err)
			}
			original := p.dungeon.runtime.Room
			calls := f.calls
			f.fail = false
			switch change {
			case "room":
				p.dungeon.runtime.Room = &runtimeDungeonRoom{}
			case "runtime":
				p.dungeon.runtime = nil
			case "character":
				p.selectedCharacterID++
			case "generation":
				p.characterGeneration++
			case "revoked":
				p.loginRevoked.Store(true)
			}
			q.now = q.now.Add(5 * time.Second)
			q.fire(q.task(t, 0), false)
			if f.calls != calls {
				t.Fatal("stale callback touched transaction")
			}
			if _, dead := original.DefeatedDeathTarget(key); dead {
				t.Fatal("stale callback killed old actor")
			}
			if len(abyssOp38Bodies(t, p.conn.(*bufferConn).write.Bytes())) != 0 {
				t.Fatal("stale death notification")
			}
		})
	}
}

func TestDeathPreparationRecoveryBackoffAndRejectedOwner(t *testing.T) {
	s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
	p := ps[0]
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	f := &deathPreparationFault{base: repos.QuestMaterialPlan, fail: true}
	repos.QuestMaterialPlan = f
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	if err := s.handleDungeonMonsterDeath(p, doc94Official249DeathBody(65000)); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || len(p.dungeon.runtime.pendingDeathPreparations) != 0 {
		t.Fatal("unknown target queued for recovery")
	}
	if err := s.armQuestMaterialRecovery(p); err != nil {
		t.Fatal(err)
	}
	if err := partyMaterialDeath(s, p, key); err != nil {
		t.Fatal(err)
	}
	calls := f.calls
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	if f.calls <= calls {
		t.Fatal("first recovery not attempted")
	}
	calls = f.calls
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 1), false)
	if f.calls != calls {
		t.Fatal("failed recovery ignored backoff")
	}
	f.fail = false
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 2), false)
	if _, dead := p.dungeon.runtime.Room.DefeatedDeathTarget(key); !dead {
		t.Fatal("backed-off death did not resume")
	}
}

func TestMonsterMaterialNegativeProofSkipsPreparation(t *testing.T) {
	s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
	source := bridgePVFSource{quest.DefaultList: "1 `unrelated.qst`\n", "n_quest/unrelated.qst": "[type]\n`[seeking]`\n[monster reward item]\n9999 -1 -1 600001 1 100 100\n[/monster reward item]\n"}
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	s.questCatalog, err = quest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	f := &deathPreparationFault{base: repos.QuestMaterialPlan, fail: true}
	repos.QuestMaterialPlan = f
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	if err := partyMaterialDeath(s, ps[0], key); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatalf("PVF-negative monster still opened %d material transactions", f.calls)
	}
	if _, dead := ps[0].dungeon.runtime.Room.DefeatedDeathTarget(key); !dead {
		t.Fatal("PVF-negative monster was blocked by unrelated preparation")
	}
}

func TestRealPVFTimeRiftWhiteMonsterRoomNoMaterialTransactions(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("real PVF not configured")
	}
	s, leader, peer, rt, _ := timeRiftBossOrderFixture(t, int64(7539), byte(2), func(s *Service, _, _ *gameSession) {
		s.dungeonChoice = func(n int) (int, error) {
			if n == 7 {
				return 6, nil
			}
			return 0, nil
		}
	})
	var err error
	s.questCatalog, err = s.loadQuestCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repos, _ := s.repositoryGroup()
	f := &deathPreparationFault{base: repos.QuestMaterialPlan, fail: true}
	repos.QuestMaterialPlan = f
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, p := range []*gameSession{leader, peer} {
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		defer s.stopGameSessionEvents(context.Background(), p)
	}
	kill := func(p *gameSession, key uint32) {
		t.Helper()
		if err := s.callGameSession(ctx, p, "rift-no-material", func() error { return partyMaterialDeath(s, p, key) }); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range rt.Room.Snapshot().Monsters {
		kill(leader, m.ObjectKey)
	}
	for _, p := range []*gameSession{leader, peer} {
		if err := s.callGameSession(ctx, p, "rift-move", func() error {
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			return s.handleDungeonMoveMapPlanned(p, body, nil, false)
		}); err != nil {
			t.Fatal(err)
		}
	}
	scene, _ := rt.Session.Scene()
	monsters := rt.Room.Snapshot().Monsters
	if scene.Map.Map.ID != 80292 || len(monsters) != 9 {
		t.Fatalf("expected exact map 80292 with9 actors: map%d count%d", scene.Map.Map.ID, len(monsters))
	}
	counts := map[int64]int{}
	for _, m := range monsters {
		counts[m.Spawn.MonsterID]++
		kill(leader, m.ObjectKey)
	}
	for _, p := range []*gameSession{leader, peer} {
		if err := s.callGameSession(ctx, p, "rift-barrier", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		sc, _ := p.dungeon.runtime.Session.Scene()
		if !sc.Cleared {
			t.Fatal("room did not clear for member")
		}
	}
	if f.calls != 0 {
		t.Fatalf("real rift still opened %d preparation transactions", f.calls)
	}
	if len(counts) != 3 || counts[107002065] != 3 || counts[107002066] != 3 || counts[107002067] != 3 {
		t.Fatal(counts)
	}
	t.Logf("map80292 actors=%v material transactions=%d; both members clear", counts, f.calls)
}
