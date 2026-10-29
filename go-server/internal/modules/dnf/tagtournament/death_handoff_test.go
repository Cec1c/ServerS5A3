package tagtournament

import (
	"testing"
	"time"
)

func TestRunDeathHandoffPreservesDeathAndOrdinaryCooldown(t *testing.T) {
	o, _, now := ownerFixture(t)
	r := admitRun(t, o, Normal)
	if changed, err := r.HandoffAfterDeath(*now); err != nil || changed {
		t.Fatal("lobby handoff", changed, err)
	}
	if _, _, err := r.BeginNext(*now, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Tag(*now, 1); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.HandoffAfterDeath(*now); err != nil || changed {
		t.Fatal("live handoff", changed, err)
	}
	if err := r.Tag(*now, 0); err == nil {
		t.Fatal("ordinary cooldown bypass")
	}
	if err := r.ApplyVitals(*now, 1, 0, 25); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.HandoffAfterDeath(now.Add(-time.Second)); err == nil || changed {
		t.Fatal("clock rollback accepted")
	}
	if changed, err := r.HandoffAfterDeath(*now); err != nil || !changed {
		t.Fatal("death stranded during cooldown", changed, err)
	}
	snapshot := r.Snapshot()
	if snapshot.Active != 0 || snapshot.Vitals[1].HP != 0 || snapshot.Vitals[1].MP != 25 {
		t.Fatal(snapshot)
	}
	if changed, err := r.HandoffAfterDeath(now.Add(time.Second)); err != nil || changed || snapshot != r.Snapshot() {
		t.Fatal("duplicate mutated run")
	}
	if err := r.Tag(now.Add(10*time.Second), 1); err == nil {
		t.Fatal("dead role revived")
	}
	if err := r.ApplyVitals(*now, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.HandoffAfterDeath(*now); err != nil || changed || r.Snapshot().Phase != Defeated {
		t.Fatal("all dead handoff", changed, err)
	}
}
