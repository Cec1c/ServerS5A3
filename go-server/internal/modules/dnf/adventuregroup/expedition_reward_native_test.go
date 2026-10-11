package adventuregroup

import (
	"testing"
	"time"
)

// Expected values were executed through the whole current EXE 0x03BF3500
// (SHA256 80f855688fe05cddd0da4b5e69bb26257e202ccee9c6fbe6efc42df483f61a90).
func TestExpeditionRewardCurrentNativeAmounts(t *testing.T) {
	config := RuntimeConfig{
		ExpeditionAreas:        []ExpeditionArea{{Index: 1, RewardRates: map[uint32]float64{21600: 1.9, 86400: 8.17}, DesignatedGroups: []string{"a", "b", "c", "d"}}},
		AttributeIDs:           map[string]byte{"a": 1, "b": 2, "c": 3, "d": 4},
		AttributeRewardRates:   []float64{1, 1, 1.4, 1.8, 2.2},
		PartyMemberRewardRates: []float64{1, 1, 1, 1, 1},
		ExpeditionExtraRate:    0.5,
	}
	for _, tc := range []struct {
		hours, count, matches int
		want                  uint32
	}{
		{6, 1, 1, 1}, {24, 1, 1, 4}, {6, 4, 1, 1}, {24, 4, 1, 4}, {24, 1, 2, 5}, {24, 1, 4, 9},
	} {
		copy := config.Clone()
		copy.CharacterAttributes = map[[2]byte][]string{{0, 1}: []string{"a", "b", "c", "d"}[:tc.matches]}
		members := make([]ExpeditionMemberInput, tc.count)
		for i := range members {
			members[i] = ExpeditionMemberInput{Level: 90, Job: 0, GrowType: 1}
		}
		got, ok := copy.ExpeditionReward(1, uint32(tc.hours*3600), members, time.Now())
		if !ok || got != tc.want {
			t.Fatalf("hours=%d count=%d matches=%d: got=%d ok=%v want=%d", tc.hours, tc.count, tc.matches, got, ok, tc.want)
		}
		for i := range members {
			members[i].Level = 1
		}
		if lower, _ := copy.ExpeditionReward(1, uint32(tc.hours*3600), members, time.Now()); lower != got {
			t.Fatal("character level changed reward amount")
		}
	}
	copy := config.Clone()
	copy.PartyMemberRewardRates[1] = 3
	if config.PartyMemberRewardRates[1] != 1 {
		t.Fatal("party rate Clone shares storage")
	}
	// Distinguish count indexing and two-stage rounding. Round(1.9*3)=6,
	// then trunc(6*.5)=3, whereas trunc(1.9*3*.5)=2.
	if got, ok := copy.ExpeditionReward(1, 21600, []ExpeditionMemberInput{{Level: 90}}, time.Now()); !ok || got != 3 {
		t.Fatalf("count rate/two-stage rounding: %d %v", got, ok)
	}
	config.PartyMemberRewardRates = nil
	if _, ok := config.ExpeditionReward(1, 21600, []ExpeditionMemberInput{{Level: 90}}, time.Now()); ok {
		t.Fatal("missing authoritative party rates accepted")
	}
}
