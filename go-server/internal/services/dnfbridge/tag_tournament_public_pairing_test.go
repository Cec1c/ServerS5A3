package dnfbridge

import (
	"context"
	"reflect"
	"testing"
)

func TestTagTournamentPublicLegacyClientKeepsTwoEntries(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, p, _, rows := tagBuffGroupsFixture(t)
	for _, setting := range []string{"", "off", "true"} {
		t.Setenv("DNFBRIDGE_TAG_BUFF_GROUP_SYNC", setting)
		got, err := s.prepareCurrentTagTournamentSkillState(context.Background(), p, rows)
		if err != nil {
			t.Fatal(err)
		}
		want := []tagBuffGroupWire{{0, 39, 1, 12, 0, 22, 22, 501}, {1, 10, 1, 12, 0, 22, 22, 511}}
		if decoded := decodeTagBuffGroups(t, got.buffSwap); !reflect.DeepEqual(decoded, want) {
			t.Fatalf("unpaired receiver got new body for %q: %+v", setting, decoded)
		}
	}
}
