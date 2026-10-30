package dnfbridge

import (
	"context"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type outerProgressionReader struct {
	dnfrepo.CharacterRepository
	full, narrow int
	denyFull     bool
}

func (r *outerProgressionReader) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.full++
	if r.denyFull {
		return dnfrepo.CharacterRecord{}, false, errors.New("unexpected full presentation read")
	}
	return r.CharacterRepository.Load(ctx, id)
}
func (r *outerProgressionReader) LoadProgressionCharacter(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.narrow++
	c, found, err := r.CharacterRepository.Load(ctx, id)
	return dnfrepo.CharacterRecord{CharacterID: c.CharacterID, AccountID: c.AccountID, Level: c.Level, Stats: c.Stats, UpdatedAt: c.UpdatedAt}, found, err
}
func (r *outerProgressionReader) SaveProgressionCharacter(context.Context, dnfrepo.CharacterRecord, dnfrepo.CharacterRecord) error {
	return errors.New("outer reader must not write")
}

func TestMonsterExperienceOrdinaryAwardOmitsOuterPresentationRead(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
	reader := &outerProgressionReader{CharacterRepository: repositories.Character, denyFull: true}
	group := repositories
	group.Character = reader
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	runtime.Character.Name = "retained dungeon actor"
	runtime.Character.Location.RoomID = "existing room"
	session := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	result, err := service.awardCurrentDungeonMonsterExperience(context.Background(), session, runtime, runtime.Room.Snapshot().Monsters[0])
	if err != nil {
		t.Fatal(err)
	}
	if reader.full != 0 || reader.narrow != 1 {
		t.Fatalf("full=%d narrow=%d", reader.full, reader.narrow)
	}
	if result.Character.Name != runtime.Character.Name || result.Character.Job != runtime.Character.Job || result.Character.Location.RoomID != runtime.Character.Location.RoomID {
		t.Fatal("narrow record replaced actor presentation")
	}
	stored, _, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || stored.Stats["exp"] != result.Character.Stats["exp"] || stored.Stats["exp"] <= 0 {
		t.Fatal("response lacks committed progression", err)
	}
}
