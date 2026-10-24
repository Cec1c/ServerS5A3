package fiendwar

import (
	"reflect"
	"testing"
	"time"
)

func TestFiendWarReservationRestoresTimersAndPreservesOtherSide(t *testing.T) {
	b, now := newFixtureBossBattle(t)
	b.Areas[30] = FiendWarAreaState{}
	b.Symbols[200] = 1
	b.GroupAreas[1] = 4
	key := FiendWarTimerKey{30, "move_enemy"}
	timer := FiendWarTimer{now, now.Add(time.Minute)}
	b.Timers[key] = timer
	b.DungeonStates[4978] = "old"
	b.Retreats[1] = 4978
	original := b.Clone()
	next, reservation, err := b.ReserveEntry(1, 4978, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Timers[key]; ok {
		t.Fatal("entry did not remove timer")
	}
	other := next.Areas[31]
	other.Deadline = now.Add(time.Hour)
	next.Areas[31] = other
	next.Symbols[203] = 42
	restored, err := next.CancelEntry(reservation)
	if err != nil {
		t.Fatal(err)
	}
	original.Areas[31] = other
	original.Symbols[203] = 42
	if !reflect.DeepEqual(restored, original) {
		t.Fatal("rollback lost preimage or another side's progress")
	}
}

func TestFiendWarReservationConflictIsAtomic(t *testing.T) {
	b, now := newFixtureBossBattle(t)
	b.Areas[30] = FiendWarAreaState{}
	b.Symbols[200] = 1
	key := FiendWarTimerKey{30, "move_enemy"}
	b.Timers[key] = FiendWarTimer{now, now.Add(time.Minute)}
	next, reservation, err := b.ReserveEntry(1, 4978, now)
	if err != nil {
		t.Fatal(err)
	}
	next.Timers[key] = FiendWarTimer{now, now.Add(time.Hour)}
	original := next.Clone()
	if restored, err := next.CancelEntry(reservation); err == nil || restored != nil {
		t.Fatal("replaced timer accepted")
	}
	if !reflect.DeepEqual(next.Clone(), original) {
		t.Fatal("conflicting cancellation partially mutated state")
	}
}
