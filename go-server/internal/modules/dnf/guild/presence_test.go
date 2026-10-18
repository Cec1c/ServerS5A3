package guild

import (
	"reflect"
	"testing"
	"time"
)

func TestGuildPresenceObservationOwnershipAndOrdering(t *testing.T) {
	reg, g, _, owner := textRegistry(t)
	before := g.Members[owner.CharacterID]
	at := before.JoinedAt.Add(time.Hour)
	if !reg.ObserveMemberOnline(owner, at) {
		t.Fatal("new observation rejected")
	}
	want := before
	want.LastOnlineAt = at
	if !reflect.DeepEqual(want, g.Members[owner.CharacterID]) {
		t.Fatal("changed unrelated member fields")
	}
	for _, stale := range []time.Time{{}, before.JoinedAt.Add(-time.Second), at.Add(-time.Second), at} {
		if reg.ObserveMemberOnline(owner, stale) || !reflect.DeepEqual(want, g.Members[owner.CharacterID]) {
			t.Fatal("stale observation replaced latest time")
		}
	}
	wrong := owner
	wrong.AccountID = "wrong"
	if reg.ObserveMemberOnline(wrong, at.Add(time.Hour)) {
		t.Fatal("wrong owner changed member")
	}
	delete(g.Members, owner.CharacterID)
	if reg.ObserveMemberOnline(owner, at.Add(time.Hour)) {
		t.Fatal("removed member resurrected")
	}
	want.JoinedAt, want.LastOnlineAt = at.Add(2*time.Hour), time.Time{}
	g.Members[owner.CharacterID] = want
	if reg.ObserveMemberOnline(owner, at.Add(time.Hour)) {
		t.Fatal("old lifecycle changed rejoined member")
	}
	if !reg.ObserveMemberOnline(owner, want.JoinedAt) {
		t.Fatal("first observation of new membership rejected")
	}
}
