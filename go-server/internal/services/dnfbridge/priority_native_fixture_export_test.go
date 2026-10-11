package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/party"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in offline native-reader fixtures. These never use player repositories.
func TestExportPriorityNativeReaderFixtures(t *testing.T) {
	dir := os.Getenv("DNF90_PRIORITY_NATIVE_FIXTURE_DIR")
	if dir == "" {
		t.Skip("offline native fixture export is opt-in")
	}
	now := time.Unix(1700000000, 0)
	state := adventuregroup.RuntimeState{Expeditions: map[string]adventuregroup.Expedition{
		"2": {Area: 2, State: 1, StartedAt: now.Unix() - 10, EndsAt: now.Unix() + 10, Attributes: []byte{4, 7},
			Members: []adventuregroup.ExpeditionMember{{CharacterID: 3, Name: "牛牛", Level: 90, Job: 1, GrowType: 2}}},
	}}
	fixtures := map[string][]byte{
		"party705.bin":            party.BuildEntryIntoPartyAck(14, 1),
		"party706.bin":            party.BuildEntryIntoPartyFinishEmptyBody(1),
		"expedition441.bin":       buildCurrentAdventureExpeditionStateBody(state, now),
		"expedition441-empty.bin": buildCurrentAdventureExpeditionStateBody(adventuregroup.RuntimeState{}, now),
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
