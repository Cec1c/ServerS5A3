package adventuregroup

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestCollectionServerGroupUsesConfiguredSourceAndRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		stored int64
		source byte
		want   byte
		valid  bool
	}{
		{0, 1, 1, true}, {0, 7, 7, true}, {2, 1, 2, true}, {255, 1, 255, true},
		{0, 0, 0, false}, {-1, 1, 0, false}, {256, 1, 0, false},
	} {
		c := dnfrepo.CharacterRecord{Stats: map[string]int64{"server_group_id": tc.stored}}
		group, valid := CollectionServerGroup(c, tc.source)
		if group != tc.want || valid != tc.valid {
			t.Fatalf("stored=%d source=%d got=%d/%t", tc.stored, tc.source, group, valid)
		}
	}
}
