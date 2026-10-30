package dnfbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// outerLevelUpProgressionReader wraps the presentation-facing character
// repository while making only a level-up load eligible to see a fake full
// record. The fake mutation is returned to the caller and is never saved to the
// backing repository.
type outerLevelUpProgressionReader struct {
	*outerProgressionReader
	mutateFull  func(dnfrepo.CharacterRecord) dnfrepo.CharacterRecord
	outerWrites int
}

func newOuterLevelUpProgressionReader(repository dnfrepo.CharacterRepository) *outerLevelUpProgressionReader {
	return &outerLevelUpProgressionReader{
		outerProgressionReader: &outerProgressionReader{CharacterRepository: repository},
	}
}

func (r *outerLevelUpProgressionReader) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	record, found, err := r.outerProgressionReader.Load(ctx, id)
	if err != nil || !found || r.mutateFull == nil {
		return record, found, err
	}
	return r.mutateFull(dnfrepo.CloneCharacter(record)), true, nil
}

func (r *outerLevelUpProgressionReader) SaveProgressionCharacter(context.Context, dnfrepo.CharacterRecord, dnfrepo.CharacterRecord) error {
	r.outerWrites++
	return errors.New("outer progression reader must not write")
}

func installOuterLevelUpProgressionTestTables(t *testing.T, service *Service) {
	t.Helper()
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source, ok := catalog.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("monster catalog source type=%T", catalog.source)
	}
	// The level-20 threshold is crossed by the fixture's ordinary 200 EXP
	// award; level 21 remains below its next threshold.
	source[progression.ExperienceTablePath] = strings.TrimSpace(strings.Repeat("0 ", 19) + "100 1000")
	source[progression.SkillPointTablePath] = "[sp table]\n20 20\n21 10\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n"
}

func TestOuterNarrowLevelUpReadsFullPresentationOnceAndKeepsLevelStats(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
	installOuterLevelUpProgressionTestTables(t, service)
	reader := newOuterLevelUpProgressionReader(repositories.Character)
	group := repositories
	group.Character = reader
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	session := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}

	result, err := service.awardCurrentDungeonMonsterExperience(
		context.Background(), session, runtime, runtime.Room.Snapshot().Monsters[0],
	)
	if err != nil {
		t.Fatal(err)
	}
	if reader.narrow != 1 || reader.full != 1 || reader.outerWrites != 0 {
		t.Fatalf("narrow=%d full=%d outer_writes=%d", reader.narrow, reader.full, reader.outerWrites)
	}
	if result.Character.Level != 21 || result.Character.Stats["exp"] != 200 || result.Character.Job != "0" {
		t.Fatalf("level-up character=%+v", result.Character)
	}
	// HP/MP vectors use tenths: (100 + 20) * 10, then compatibility bonus.
	for key, want := range map[string]int64{
		"stat_hp_max":          11000,
		"stat_mp_max":          11700,
		"stat_physical_attack": 30,
	} {
		if got := result.Character.Stats[key]; got != want {
			t.Fatalf("level-21 %s=%d want %d", key, got, want)
		}
	}
	if result.SPGain != 10 || result.Skill.Points != (dnfrepo.SkillPointState{
		TotalSP: 30, RemainingSP: 30, SyncedLevel: 21,
	}) {
		t.Fatalf("sp_gain=%d skill=%+v", result.SPGain, result.Skill.Points)
	}

	persisted, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("persisted character found=%t err=%v", found, err)
	}
	if persisted.Level != 21 || persisted.Stats["exp"] != 200 || persisted.Job != "0" ||
		persisted.Stats["stat_hp_max"] != 11000 || persisted.Stats["stat_mp_max"] != 11700 ||
		persisted.Stats["stat_physical_attack"] != 30 {
		t.Fatalf("persisted level-up character=%+v", persisted)
	}
}

func TestOuterNarrowLevelUpRejectsFullReadOnlyChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*dnfrepo.CharacterRecord)
	}{
		{
			name: "account changed",
			mutate: func(record *dnfrepo.CharacterRecord) {
				record.AccountID = "account-2"
			},
		},
		{
			name: "level changed",
			mutate: func(record *dnfrepo.CharacterRecord) {
				record.Level = 21
			},
		},
		{
			name: "experience changed",
			mutate: func(record *dnfrepo.CharacterRecord) {
				record.Stats["exp"] = 1
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
			installOuterLevelUpProgressionTestTables(t, service)
			reader := newOuterLevelUpProgressionReader(repositories.Character)
			reader.mutateFull = func(record dnfrepo.CharacterRecord) dnfrepo.CharacterRecord {
				test.mutate(&record)
				return record
			}
			group := repositories
			group.Character = reader
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
			session := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}

			_, err := service.awardCurrentDungeonMonsterExperience(
				context.Background(), session, runtime, runtime.Room.Snapshot().Monsters[0],
			)
			if !errors.Is(err, errCurrentDungeonMonsterExperienceStateInvalid) {
				t.Fatalf("error=%v", err)
			}
			if reader.narrow != 1 || reader.full != 1 || reader.outerWrites != 0 {
				t.Fatalf("narrow=%d full=%d outer_writes=%d", reader.narrow, reader.full, reader.outerWrites)
			}
			persisted, found, err := repositories.Character.Load(context.Background(), "99")
			if err != nil || !found {
				t.Fatalf("persisted character found=%t err=%v", found, err)
			}
			if persisted.AccountID != "account-1" || persisted.Level != 20 || persisted.Stats["exp"] != 0 {
				t.Fatalf("fake full-read change reached storage: %+v", persisted)
			}
			skill, found, err := repositories.Skill.Load(context.Background(), "99")
			if err != nil || !found || skill.Points != (dnfrepo.SkillPointState{
				TotalSP: 20, RemainingSP: 20, SyncedLevel: 20,
			}) {
				t.Fatalf("rejected level-up changed skill=%+v found=%t err=%v", skill, found, err)
			}
		})
	}
}
