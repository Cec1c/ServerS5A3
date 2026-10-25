package dnfbridge

import (
	"bytes"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func r195CoinProjectionValues(t *testing.T, wire []byte) []byte {
	t.Helper()
	var values []byte
	for len(wire) > 0 {
		p, rest := splitGameServerUpperPacket(t, wire)
		wire = rest
		if p.Header.MsgID == 775 {
			if p.Header.Classification != 0 || len(p.Body) != 1 {
				t.Fatalf("invalid native coin notification: %+v %x", p.Header, p.Body)
			}
			values = append(values, p.Body[0])
		}
	}
	return values
}

func TestR195LukeCoinProjectionFreshReconnectLatestAndPhase(t *testing.T) {
	f, s, _ := normalLukeFixture(t)
	b := f.state.Battle
	for _, step := range []struct {
		phase              byte
		used               uint32
		fresh, resubscribe bool
		want               []byte
	}{
		{0, 0, true, false, []byte{0}},
		{0, 0, false, true, []byte{0}},
		{1, 0, true, false, []byte{0}},
		{1, 19, false, false, []byte{19}},
		{1, 20, false, true, []byte{20}},
	} {
		b.Phase, b.PhaseCoins[step.phase] = step.phase, step.used
		b.ProgressVersion++
		f.conn.write.Reset()
		if err := f.service.sendCurrentRaidProgressProjection(s, cloneRuntimeRaidState(*f.state), step.fresh, step.resubscribe); err != nil {
			t.Fatal(err)
		}
		if got := r195CoinProjectionValues(t, f.conn.write.Bytes()); !bytes.Equal(got, step.want) {
			t.Fatalf("phase %d count %d: got %v want %v", step.phase, step.used, got, step.want)
		}
	}
	old := cloneRuntimeRaidState(*f.state)
	b.PhaseCoins[1], b.ProgressVersion = 7, b.ProgressVersion+1
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidProgressProjection(s, old, false, false); err != nil {
		t.Fatal(err)
	}
	if got := r195CoinProjectionValues(t, f.conn.write.Bytes()); !bytes.Equal(got, []byte{7}) {
		t.Fatal("stale snapshot rolled count back", got)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidProgressProjection(s, old, false, false); err != nil {
		t.Fatal(err)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("duplicate snapshot resent")
	}
	b.PhaseCoins[1] = 256
	b.ProgressVersion++
	if err := f.service.sendCurrentRaidProgressProjection(s, cloneRuntimeRaidState(*f.state), false, false); err == nil {
		t.Fatal("overflow count wrapped")
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("invalid count sent partial map")
	}
}

func TestR195LukeCoinProjectionCommitAndRollback(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixtureForDungeon(t, false, 5311)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	reviewLukeCoinDie(t, f, s)
	ctx := t.Context()
	beforeVersion := f.state.Battle.ProgressVersion
	s.dungeon.mu.Lock()
	result, err := f.service.consumeCurrentDungeonReviveCoinWithLukeCaps(ctx, s, r, s.selectedCharacterID)
	s.dungeon.mu.Unlock()
	if err != nil || result.RaidCoinProjection == nil || !result.Consumed {
		t.Fatal("missing committed coin projection", err)
	}
	if f.state.Battle.ProgressVersion != beforeVersion+1 || result.RaidCoinProjection.Battle.PhaseCoins[1] != 1 {
		t.Fatal("committed projection has wrong count/version")
	}
	repos, _ := f.service.repositoryGroup()
	failed := repos
	failed.CharacterItems = reviewLukeLateFailItems{repos.CharacterItems}
	f.service.repositoryProvider = func() (repo.Group, bool) { return failed, true }
	s.dungeon.mu.Lock()
	result, err = f.service.consumeCurrentDungeonReviveCoinWithLukeCaps(ctx, s, r, s.selectedCharacterID)
	s.dungeon.mu.Unlock()
	if err == nil || result.RaidCoinProjection != nil || f.state.Battle.ProgressVersion != beforeVersion+1 || f.state.Battle.PhaseCoins[1] != 1 {
		t.Fatal("failed transaction published/incremented count", err)
	}
}
