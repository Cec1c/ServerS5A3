package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Execute the real-PVF rule and repository-to-roster path. The actual native
// flag reader/selector branch is covered by the exported fixtures.
func TestRealPVFOfficialRosterChannelPermissions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the exact candidate PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := dnfquest.LoadSpecialChannelRule(archive)
	if err != nil {
		t.Fatal(err)
	}
	if rule.AntonQuestID != 12829 || rule.LukeQuestID != 2903 || rule.FiendWarQuestID != 8641 {
		t.Fatalf("actual entry quests changed: %+v", rule)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	s := &Service{options: options{currentClientVersion: "official-24.9"}, initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	ctx := context.Background()
	for _, tc := range []struct {
		name               string
		anton, luke, fiend string
		want               []byte
	}{
		{"missing", "", "", "", []byte{0, 0, 0, 0}},
		{"pending_finish", "active", "active", "active", []byte{0, 0, 0, 0}},
		{"test1_completed_anton", "completed", "active", "", []byte{1, 0, 0, 0}},
		{"completed_luke", "active", "completed", "", []byte{0, 1, 0, 0}},
		{"completed_both", "completed", "completed", "", []byte{1, 1, 0, 0}},
		{"completed_fiendwar", "", "", "completed", []byte{0, 0, 0, 1}},
		{"completed_all_supported", "completed", "completed", "completed", []byte{1, 1, 0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{12829: {Status: tc.anton}, 2903: {Status: tc.luke, ProgressValue: 0}, 8641: {Status: tc.fiend, ProgressValue: 0}}}
			if err := repos.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			character := dnfrepo.CharacterRecord{CharacterID: "1", Name: "test", Level: 90, Job: "0"}
			base := buildNoPackRosterBodyWithEntry([]dnfrepo.CharacterRecord{character}, writeOfficial249RosterEntry)
			got := s.buildCurrentRosterBody([]dnfrepo.CharacterRecord{character})
			flagAt := len(got) - 10 - 4
			if len(got) != len(base) || !bytes.Equal(got[:flagAt], base[:flagAt]) || !bytes.Equal(got[flagAt+4:], base[flagAt+4:]) || !bytes.Equal(got[flagAt:flagAt+4], tc.want) {
				t.Fatalf("wrong native roster flag projection: %x", got)
			}
			after, _, err := repos.Quest.Load(ctx, "1")
			if err != nil || !reflect.DeepEqual(after, record) {
				t.Fatal("readonly roster changed quest state")
			}
			if dir := os.Getenv("DNF_ROSTER_PERMISSION_NATIVE_EXPORT"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tc.name+".bin"), got, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	// Another character's completed quests must not grant this one's flags.
	other := dnfrepo.CharacterRecord{CharacterID: "2", Name: "other", Level: 90, Job: "0"}
	got := s.buildCurrentRosterBody([]dnfrepo.CharacterRecord{other})
	if !bytes.Equal(got[len(got)-14:len(got)-10], []byte{0, 0, 0, 0}) {
		t.Fatal("completed quests crossed character boundary")
	}
}
