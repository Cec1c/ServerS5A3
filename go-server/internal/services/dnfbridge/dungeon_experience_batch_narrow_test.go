package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestBatchNarrowProgressionOrdinaryAndCapMatchLegacy(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "cap"}[capped], func(t *testing.T) {
			var expected r.CharacterRecord
			for _, narrow := range []bool{false, true} {
				s, p, repos, _ := prepareDungeonDeathBurstTest(t, capped)
				reader := &outerProgressionReader{CharacterRepository: repos.Character, denyFull: true}
				if narrow {
					repos.Character = reader
					s.repositoryProvider = func() (r.Group, bool) { return repos, true }
				}
				resources, err := s.currentDungeonExperienceCommitResources()
				if err != nil {
					t.Fatal(err)
				}
				items := make([]currentDungeonDeathBurstAward, 3)
				for i := range items {
					items[i] = currentDungeonDeathBurstAward{tables: resources.Progression, result: currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: 10}}}
				}
				results, err := s.commitCurrentDungeonExperienceBatch(context.Background(), p, p.dungeon.runtime, items)
				if err != nil {
					t.Fatal(err)
				}
				if narrow && (reader.narrow != 1 || reader.full != 0) {
					t.Fatal(reader.narrow, reader.full)
				}
				for _, result := range results {
					if result.Character.Job != p.dungeon.runtime.Character.Job || result.Character.Name != p.dungeon.runtime.Character.Name {
						t.Fatal("narrow replaced presentation")
					}
				}
				stored, _, err := reader.CharacterRepository.Load(context.Background(), "99")
				if err != nil {
					t.Fatal(err)
				}
				stored.UpdatedAt = time.Time{}
				if !narrow {
					expected = stored
				} else if !reflect.DeepEqual(expected, stored) {
					t.Fatal("narrow and legacy final state differ")
				}
			}
		})
	}
}

func TestBatchNarrowProgressionLaterLevelBoundaryAndConcurrentChanges(t *testing.T) {
	for _, mutation := range []string{"none", "owner", "level", "experience", "version", "other stat"} {
		t.Run(mutation, func(t *testing.T) {
			s, _, repos := prepareCurrentDungeonMonsterExperienceTest(t)
			installOuterLevelUpProgressionTestTables(t, s)
			reader := newOuterLevelUpProgressionReader(repos.Character)
			reader.mutateFull = func(c r.CharacterRecord) r.CharacterRecord {
				switch mutation {
				case "owner":
					c.AccountID = "other"
				case "level":
					c.Level++
				case "experience":
					c.Stats["exp"]++
				case "version":
					c.UpdatedAt = c.UpdatedAt.Add(time.Second)
				case "other stat":
					c.Stats["gold"]++
				}
				return c
			}
			group := repos
			group.Character = reader
			s.repositoryProvider = func() (r.Group, bool) { return group, true }
			full, _, _ := repos.Character.Load(context.Background(), "99")
			runtime := &runtimeDungeonState{Character: full}
			p := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			resources, err := s.currentDungeonExperienceCommitResources()
			if err != nil {
				t.Fatal(err)
			}
			items := []currentDungeonDeathBurstAward{{tables: resources.Progression, result: currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: 60}}}, {tables: resources.Progression, result: currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: 60}}}}
			results, err := s.commitCurrentDungeonExperienceBatch(context.Background(), p, runtime, items)
			stored, _, _ := repos.Character.Load(context.Background(), "99")
			if reader.narrow != 1 || reader.full != 1 || reader.outerWrites != 0 {
				t.Fatal(reader.narrow, reader.full, reader.outerWrites)
			}
			if mutation != "none" {
				if !errors.Is(err, errCurrentDungeonMonsterExperienceStateInvalid) || stored.Level != 20 || stored.Stats["exp"] != 0 {
					t.Fatal(err, stored)
				}
				return
			}
			if err != nil || len(results) != 2 || results[0].Character.Level != 20 || results[1].Character.Level != 21 || stored.Level != 21 || stored.Stats["exp"] != 120 {
				t.Fatal(err, results, stored)
			}
		})
	}
}
