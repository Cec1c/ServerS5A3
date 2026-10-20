package raid

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func fiendWarMovementFixture(t *testing.T) (*FiendWarMovement, []FiendWarParticipant, time.Time) {
	t.Helper()
	p, err := LoadFiendWarProgram(realFiendWarSource(t))
	if err != nil {
		t.Fatal(err)
	}
	var members []FiendWarParticipant
	for i := 0; i < 8; i++ {
		members = append(members, FiendWarParticipant{CharacterID: fmt.Sprintf("character-%d", i), AccountID: fmt.Sprintf("account-%d", i), GroupID: uint32(i/4 + 1)})
	}
	start := time.Unix(1800000000, 0)
	s, err := p.BeginMovement("00112233445566778899aabbccddeeff", members, start)
	if err != nil {
		t.Fatal(err)
	}
	return s, members, start
}

func confirmFiendWarGroup(t *testing.T, s *FiendWarMovement, run string, members []FiendWarParticipant, now time.Time) *FiendWarMovement {
	t.Helper()
	for index, m := range members {
		n, committed, err := s.ConfirmMove(s.instance, run, m, now)
		if err != nil || committed != (index == len(members)-1) {
			t.Fatal("all-member barrier", index, committed, err)
		}
		s = n
	}
	return s
}

func TestRealPVFFiendWarMovementReservationsCapacityAndAllMemberBarrier(t *testing.T) {
	s, m, start := fiendWarMovementFixture(t)
	original := s.clone()
	const run1 = "10000000000000000000000000000001"
	n, err := s.PrepareMove(s.instance, run1, m[0], 2, 3, start)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, original) || len(n.pending) != 1 {
		t.Fatal("reservation mutated published state")
	}
	if _, err := n.PrepareMove(n.instance, "20000000000000000000000000000001", m[4], 2, 3, start); err == nil {
		t.Fatal("reserved capacity allowed second four-member group")
	}
	repeated, err := n.PrepareMove(n.instance, run1, m[1], 2, 3, start)
	if err != nil || len(repeated.pending) != 1 {
		t.Fatal("same group reservation replay", err)
	}
	first, committed, err := n.ConfirmMove(n.instance, run1, m[0], start)
	if err != nil || committed {
		t.Fatal(err)
	}
	first, committed, err = first.ConfirmMove(first.instance, run1, m[0], start)
	if err != nil || committed || len(first.pending[run1].confirmed) != 1 {
		t.Fatal("member confirmed twice", err)
	}
	for _, member := range m {
		if area, _ := first.Location(member.CharacterID); area != 2 {
			t.Fatal("premature partial group move")
		}
	}
	n = confirmFiendWarGroup(t, n, run1, m[:4], start)
	for _, member := range m[:4] {
		if area, _ := n.Location(member.CharacterID); area != 3 {
			t.Fatal("group entry did not commit together")
		}
	}
	if _, _, err := n.ConfirmMove(n.instance, run1, m[0], start); err == nil {
		t.Fatal("completed movement confirmation replay accepted")
	}
	const run2 = "10000000000000000000000000000002"
	n, err = n.PrepareMove(n.instance, run2, m[0], 3, 6, start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	enteredAt := start.Add(7 * time.Second)
	n = confirmFiendWarGroup(t, n, run2, m[:4], enteredAt)
	if len(n.active) != 1 || !n.active[run2].end.Equal(enteredAt.Add(time.Duration(n.areas[6].LimitSeconds)*time.Second)) {
		t.Fatal("dungeon clock did not start at full entry")
	}
	projection, ok := n.AreaRun(run2)
	if !ok || projection.DungeonID != n.areas[6].DungeonID || len(projection.Participants) != 4 || projection.Deadline != n.active[run2].end {
		t.Fatal("owned dungeon binding snapshot missing")
	}
	projection.Participants[0].AccountID = "changed-outside-owner"
	unchanged, _ := n.AreaRun(run2)
	if unchanged.Participants[0].AccountID == "changed-outside-owner" {
		t.Fatal("caller mutated frozen run identity")
	}
	if _, err := n.PrepareMove(n.instance, "10000000000000000000000000000003", m[0], 6, 10, enteredAt); err == nil {
		t.Fatal("active area run permitted next entry")
	}
	n, err = n.ReleaseAreaRun(n.instance, run2, m[1], enteredAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = n.PrepareMove(n.instance, "10000000000000000000000000000003", m[0], 6, 10, enteredAt); err != nil {
		t.Fatal("owned completed run did not release movement", err)
	}
	if _, err = n.PrepareMove(n.instance, "20000000000000000000000000000001", m[4], 2, 3, enteredAt); err != nil {
		t.Fatal("old origin capacity not released", err)
	}
}

func TestRealPVFFiendWarMovementRejectsForeignOwnersEdgesAndClocks(t *testing.T) {
	s, m, start := fiendWarMovementFixture(t)
	const run = "10000000000000000000000000000001"
	for name, input := range map[string]struct {
		instance string
		actor    FiendWarParticipant
		from, to uint32
		now      time.Time
	}{
		"foreign-instance":      {"ffeeddccbbaa99887766554433221100", m[0], 2, 3, start},
		"foreign-account":       {s.instance, FiendWarParticipant{m[0].CharacterID, "other-account", m[0].GroupID}, 2, 3, start},
		"foreign-group":         {s.instance, FiendWarParticipant{m[0].CharacterID, m[0].AccountID, 2}, 2, 3, start},
		"unknown-character":     {s.instance, FiendWarParticipant{"unknown", "unknown", 1}, 2, 3, start},
		"foreign-area":          {s.instance, m[0], 2, 999, start},
		"nonadjacent-real-area": {s.instance, m[0], 2, 6, start},
		"wrong-origin":          {s.instance, m[0], 3, 6, start},
		"backdated":             {s.instance, m[0], 2, 3, start.Add(-time.Second)},
		"phase-deadline":        {s.instance, m[0], 2, 3, s.deadline},
	} {
		t.Run(name, func(t *testing.T) {
			before := s.clone()
			if _, err := s.PrepareMove(input.instance, run, input.actor, input.from, input.to, input.now); err == nil {
				t.Fatal("invalid movement accepted")
			}
			if !reflect.DeepEqual(before, s) {
				t.Fatal("failed movement mutated state")
			}
		})
	}
	n, err := s.PrepareMove(s.instance, run, m[0], 2, 3, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = n.ConfirmMove(n.instance, run, m[4], start); err == nil {
		t.Fatal("another group confirmed prepared run")
	}
	if _, err = n.CancelPreparedMove(n.instance, run, m[4], start); err == nil {
		t.Fatal("another group cancelled prepared run")
	}
	n, err = n.CancelPreparedMove(n.instance, run, m[1], start)
	if err != nil || len(n.pending) != 0 {
		t.Fatal(err)
	}
	if _, err = n.PrepareMove(n.instance, run, m[0], 2, 3, start); err == nil {
		t.Fatal("cancelled run reused")
	}
	if _, err = n.PrepareMove(n.instance, "20000000000000000000000000000001", m[4], 2, 3, start); err != nil {
		t.Fatal("cancellation retained area capacity", err)
	}
	closed, err := s.Tick(s.deadline)
	if err != nil || !closed.closed || len(closed.pending) != 0 {
		t.Fatal(err)
	}
	if _, err = closed.PrepareMove(closed.instance, run, m[0], 2, 3, s.deadline); err == nil {
		t.Fatal("closed phase accepts entry")
	}
}

func TestRealPVFFiendWarMovementFrozenRosterValidation(t *testing.T) {
	p, err := LoadFiendWarProgram(realFiendWarSource(t))
	if err != nil {
		t.Fatal(err)
	}
	_, m, start := fiendWarMovementFixture(t)
	for name, members := range map[string][]FiendWarParticipant{
		"empty":               nil,
		"duplicate-account":   {{"one", "same", 1}, {"two", "same", 1}},
		"duplicate-character": {{"same", "one", 1}, {"same", "two", 1}},
		"no-group":            {{"one", "one", 0}},
		"overfull-group":      {{"one", "one", 1}, {"two", "two", 1}, {"three", "three", 1}, {"four", "four", 1}, {"five", "five", 1}},
		"overfull-raid":       append(append([]FiendWarParticipant(nil), m...), FiendWarParticipant{"ninth", "ninth", 3}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.BeginMovement("00112233445566778899aabbccddeeff", members, start); err == nil {
				t.Fatal("invalid frozen roster accepted")
			}
		})
	}
}
