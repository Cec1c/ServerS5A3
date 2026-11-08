package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// firstAcceptQuestWithBaselineSavePath hides mysqlQuestStore's QuestDeltaWriter
// implementation. repository.SaveQuestChanges therefore uses the original
// SaveQuestFields path, matching the pre-PR37 owner behavior while retaining
// the real fake-SQL MySQL writer beneath it.
type firstAcceptQuestWithBaselineSavePath struct {
	repository.QuestRepository
	writer *mysqlQuestStore
}

func (r firstAcceptQuestWithBaselineSavePath) SaveFields(ctx context.Context, record repository.QuestRecord, fields ...repository.QuestField) error {
	return r.writer.SaveFields(ctx, record, fields...)
}

type firstAcceptPVFSource map[string]string

func (s firstAcceptPVFSource) ReadText(relativePath string) (string, error) {
	value, ok := s[relativePath]
	if !ok {
		return "", errors.New("missing review PVF path: " + relativePath)
	}
	return value, nil
}

func firstAcceptAcceptCatalog(t *testing.T) *quest.Catalog {
	t.Helper()
	source := firstAcceptPVFSource{
		quest.DefaultList: "3145 `accept.qst`\n",
		"n_quest/accept.qst": "[grade]\n`[epic]`\n[level]\n1 99\n[job]\n`[gunner]`\n[exposed by npc]\n1\n" +
			"[type]\n`[clear map]`\n[int data]\n76126\n",
	}
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// Exercise the production owner with a fresh character and absent SQL parent.
func TestMySQLQuestOwnerFirstAcceptPathContract(t *testing.T) {
	catalog := firstAcceptAcceptCatalog(t)

	t.Run("baseline_save_fields_creates_missing_parent", func(t *testing.T) {
		runFirstAcceptFirstAccept(t, catalog, false)
	})
	t.Run("candidate_save_quest_changes_creates_missing_parent", func(t *testing.T) {
		runFirstAcceptFirstAccept(t, catalog, true)
	})
}

func runFirstAcceptFirstAccept(t *testing.T, catalog *quest.Catalog, useDelta bool) {
	t.Helper()
	ctx := context.Background()
	memoryRepos := memory.NewMemoryGroup()
	_, _, router := newDeathDeltaSQL(t)
	sqlRepos := newMySQLGroupFromRouter(router, false)
	writer, ok := sqlRepos.Quest.(*mysqlQuestStore)
	if !ok {
		t.Fatalf("quest repository type = %T, want *mysqlQuestStore", sqlRepos.Quest)
	}

	questRepo := repository.QuestRepository(writer)
	if !useDelta {
		questRepo = firstAcceptQuestWithBaselineSavePath{QuestRepository: writer, writer: writer}
	}
	if _, delta := questRepo.(repository.QuestDeltaWriter); delta != useDelta {
		t.Fatalf("QuestDeltaWriter capability = %t, want %t", delta, useDelta)
	}

	const (
		accountID   = "firstAccept-account"
		characterID = uint16(17)
		questID     = uint16(3145)
	)
	if err := memoryRepos.Character.Save(ctx, repository.CharacterRecord{
		CharacterID: "17",
		AccountID:   accountID,
		Job:         "2",
		Level:       1,
	}); err != nil {
		t.Fatal(err)
	}

	// A fresh character fixture has no quest parent. Deliberately do not seed
	// one: production character creation likewise omits Creation.Quest.
	if _, found, err := questRepo.Load(ctx, "17"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("quest parent unexpectedly pre-existed")
	}

	group := memoryRepos
	group.Quest = questRepo
	owner, err := quest.NewOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyAcceptAt(ctx, catalog, quest.CharacterEligibility{Level: 1, Job: 2}, quest.Command{
		Operation:           "accept_quest",
		AccountID:           accountID,
		SelectedCharacterID: characterID,
		QuestID:             questID,
		NeedsOwner:          "PR37 first-accept parent creation regression",
	}, time.Unix(10000, 0).UTC())
	if err != nil {
		if errors.Is(err, repository.ErrQuestVersionConflict) {
			t.Fatalf("first accept failed because the quest parent was absent (PR37 SaveQuestChanges regression): %v", err)
		}
		t.Fatalf("ApplyAcceptAt: %v", err)
	}
	if result.QuestID != questID || result.InitTrigger != 1 || result.Idempotent {
		t.Fatalf("ApplyAcceptAt result = %+v", result)
	}
	persisted, found, err := questRepo.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("quest parent was not created")
	}
	state, ok := persisted.States[int64(questID)]
	if !ok || state.Status != "active" || state.ProgressValue != 1 || state.Extra["pvf_path"] == "" {
		t.Fatalf("persisted first quest state = %+v, found=%t", state, ok)
	}
}
