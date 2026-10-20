package quest

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

type epicForeignCharacter struct{ repo.CharacterRepository }

func (r epicForeignCharacter) Load(ctx context.Context, id string) (repo.CharacterRecord, bool, error) {
	v, ok, e := r.CharacterRepository.Load(ctx, id)
	v.CharacterID = "99"
	return v, ok, e
}

type epicForeignQuest struct {
	repo.QuestRepository
	writes *int
}

func (r epicForeignQuest) Load(ctx context.Context, id string) (repo.QuestRecord, bool, error) {
	return repo.QuestRecord{CharacterID: "99"}, true, nil
}
func (r epicForeignQuest) Save(ctx context.Context, v repo.QuestRecord) error {
	*r.writes++
	return r.QuestRepository.Save(ctx, v)
}

func TestActualEpic350AcceptRejectsForeignPhysicalOwner(t *testing.T) {
	c := loadRealPVFQuestCatalog(t)
	eligibility, e := c.WithEpicRoadEvent350Eligibility(CharacterEligibility{Level: 90, Job: 0}, true)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"wrong_account", "wrong_character_record", "wrong_quest_record"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "owned", Level: 90, Job: "0"})
			cmd := Command{SelectedCharacterID: 31, AccountID: "owned", QuestID: 5578}
			writes := 0
			if kind == "wrong_account" {
				cmd.AccountID = "foreign"
			}
			if kind == "wrong_character_record" {
				g.Character = epicForeignCharacter{g.Character}
			}
			if kind == "wrong_quest_record" {
				g.Quest = epicForeignQuest{g.Quest, &writes}
			}
			o, e := NewOwner(g)
			if e != nil {
				t.Fatal(e)
			}
			_, e = o.ApplyAccept(ctx, c, eligibility, cmd)
			if e == nil {
				t.Fatal("foreign owner accepted actual event quest")
			}
			if writes != 0 {
				t.Fatal("foreign record reached Save")
			}
			record, _, _ := g.Quest.Load(ctx, "31")
			if _, exists := record.States[5578]; exists {
				t.Fatal("rejected quest persisted")
			}
		})
	}
}
