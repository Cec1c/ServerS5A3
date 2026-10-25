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
)

// 022BFC10 reads the final pre-raid byte; bit zero becomes selector +319.
// Verify the actual repository/official-profile path, including a character
// with the entrance complete but the later Windbell training still active.
func TestOfficial249PVPRosterEntranceUsesPersistedQuest(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		progress     bool
		want         byte
	}{
		{"missing", "", false, 0},
		{"active", "active", false, 0},
		{"completed_entrance_training_pending", "completed", false, 1},
		{"done", "done", false, 1},
		{"cleared", "cleared", false, 1},
		{"legacy_progress_completed", "completed", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "pvp-entry", Name: "entry", Slot: 1, Job: "14", Level: 90}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			quest := dnfrepo.QuestRecord{CharacterID: "2", States: map[int64]dnfrepo.QuestState{2672: {Status: "active", ProgressValue: 1}}, Progress: map[int64]dnfrepo.QuestState{}}
			if tc.status != "" {
				if tc.progress {
					quest.Progress[2670] = dnfrepo.QuestState{Status: tc.status}
				} else {
					quest.States[2670] = dnfrepo.QuestState{Status: tc.status}
				}
			}
			if err := repos.Quest.Save(ctx, quest); err != nil {
				t.Fatal(err)
			}
			before, found, err := repos.Quest.Load(ctx, "2")
			if err != nil || !found {
				t.Fatalf("保存后读取任务失败：%v", err)
			}
			s := &Service{options: options{currentClientVersion: "official-24.9"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			characters, err := s.listCharacters(ctx, repos, "pvp-entry")
			if err != nil || len(characters) != 1 {
				t.Fatalf("角色列表读取失败：%v %+v", err, characters)
			}
			body := s.buildCurrentRosterBody(characters)
			at := len(body) - 10 - 4 - 1
			if body[at] != tc.want {
				t.Fatalf("现用角色列表入口任务标志=%d，期望%d；任务=%+v", body[at], tc.want, quest)
			}
			baseline := characters[0]
			baseline.Roster.PVPEntryQuestCompleted = false
			base := s.buildCurrentRosterBody([]dnfrepo.CharacterRecord{baseline})
			if len(body) != len(base) || !bytes.Equal(body[:at], base[:at]) || !bytes.Equal(body[at+1:], base[at+1:]) {
				t.Fatal("入口标志之外的现用角色列表字节发生变化")
			}
			after, found, err := repos.Quest.Load(ctx, "2")
			if err != nil || !found || !reflect.DeepEqual(after, before) {
				t.Fatal("角色列表投影改变了任务记录")
			}
			if dir := os.Getenv("DNF_PVP_ENTRY_NATIVE_EXPORT"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tc.name+".bin"), body, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOfficial249PVPEntrancePreservesRaidFlagsAndCharacterBoundaries(t *testing.T) {
	characters := []dnfrepo.CharacterRecord{{CharacterID: "1", Name: "allowed", Slot: 0, Level: 90}, {CharacterID: "2", Name: "pending", Slot: 1, Level: 90}}
	characters[0].Roster.PVPEntryQuestCompleted = true
	permission := dnfquest.SpecialChannelPermission{Anton: true, Luke: true, FiendWar: true}
	var first, second packetWriter
	writeOfficial249RosterEntryWithPermission(&first, 0, characters[0], permission)
	writeOfficial249RosterEntryWithPermission(&second, 1, characters[1], permission)
	// Four native raid flags remain after the independent PVP entrance byte.
	a, b := first.bytes(), second.bytes()
	if !bytes.Equal(a[len(a)-5:], []byte{1, 1, 1, 0, 1}) || !bytes.Equal(b[len(b)-5:], []byte{0, 1, 1, 0, 1}) {
		t.Fatalf("入口或团本标志串用：%x / %x", a[len(a)-5:], b[len(b)-5:])
	}
}
