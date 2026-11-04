package dnfbridge

import (
	"context"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
	"time"
)

type deathPreparationFault struct {
	base            r.QuestMaterialPreparationUnitOfWork
	fail            bool
	calls           int
	failAfterCommit bool
}

func (f *deathPreparationFault) WithinQuestMaterialPreparation(ctx context.Context, ids []string, apply func(r.Group) error) error {
	f.calls++
	if f.fail {
		return context.DeadlineExceeded
	}
	err := f.base.WithinQuestMaterialPreparation(ctx, ids, apply)
	if err == nil && f.failAfterCommit {
		f.failAfterCommit = false
		return context.DeadlineExceeded
	}
	return err
}

func TestDeathPreparationRecoveryAfterAmbiguousCommitOrPeerDeath(t *testing.T) {
	for _, peerFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(peerFirst), func(t *testing.T) {
			s, ps, repos, key := partyMaterialFixture(t, 2, 0, false, false, 100)
			q := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = q
			fault := &deathPreparationFault{base: repos.QuestMaterialPlan, fail: peerFirst, failAfterCommit: !peerFirst}
			repos.QuestMaterialPlan = fault
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			if err := s.armQuestMaterialRecovery(ps[0]); err != nil {
				t.Fatal(err)
			}
			if err := partyMaterialDeath(s, ps[0], key); err != nil {
				t.Fatal(err)
			}
			// An ambiguous successful commit can already be resolved by the
			// durable material planner's readback; an actual failure stays pending.
			if _, dead := ps[0].dungeon.runtime.Room.DefeatedDeathTarget(key); dead == peerFirst {
				t.Fatal("death state disagrees with durable preparation outcome")
			}
			fault.fail = false
			if peerFirst {
				if err := partyMaterialDeath(s, ps[1], key); err != nil {
					t.Fatal(err)
				}
			}
			q.now = q.now.Add(5 * time.Second)
			q.fire(q.task(t, 0), false)
			for _, p := range ps {
				if err := partyMaterialDeath(s, p, key); err != nil {
					t.Fatal(err)
				}
				inv, _, err := repos.Inventory.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
				if err != nil || currentQuestDropHeldCount(inv, 600001) != 1 {
					t.Fatalf("member%d grant not exactly once: %v", p.selectedCharacterID, err)
				}
				// Party duplicate reports deliberately replay the same death ACK.
				// Material quantity above, rather than ACK count, proves no regrant.
				if len(abyssOp38Bodies(t, p.conn.(*bufferConn).write.Bytes())) == 0 {
					t.Fatalf("member%d missing death notification", p.selectedCharacterID)
				}
			}
			if len(ps[0].dungeon.runtime.pendingDeathPreparations) != 0 {
				t.Fatal("recovered pending death retained")
			}
		})
	}
}

func TestDeathPreparationRecoveryAfterTimeout(t *testing.T) {
	s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
	p := ps[0]
	p.connID = "preparation-recovery"
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	fault := &deathPreparationFault{base: repos.QuestMaterialPlan, fail: true}
	repos.QuestMaterialPlan = fault
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	if err := s.armQuestMaterialRecovery(p); err != nil {
		t.Fatal(err)
	}
	if err := partyMaterialDeath(s, p, key); err != nil {
		t.Fatal(err)
	}
	if _, dead := p.dungeon.runtime.Room.DefeatedDeathTarget(key); dead {
		t.Fatal("death committed before transaction")
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("death acknowledged before transaction")
	}
	fault.fail = false
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	if _, dead := p.dungeon.runtime.Room.DefeatedDeathTarget(key); !dead {
		t.Fatal("one transient preparation timeout permanently stranded the monster without a second client report")
	}
	inv, _, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil || currentQuestDropHeldCount(inv, 600001) != 1 {
		t.Fatalf("recovered material missing: %v", err)
	}
	before := len(abyssOp38Bodies(t, p.conn.(*bufferConn).write.Bytes()))
	if before != 1 {
		t.Fatalf("death notifications=%d want1", before)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 1), false)
	if err := partyMaterialDeath(s, p, key); err != nil {
		t.Fatal(err)
	}
	inv, _, _ = repos.Inventory.Load(context.Background(), "99")
	if currentQuestDropHeldCount(inv, 600001) != 1 || len(abyssOp38Bodies(t, p.conn.(*bufferConn).write.Bytes())) != before {
		t.Fatal("duplicate recovery repeated death or material")
	}
}
