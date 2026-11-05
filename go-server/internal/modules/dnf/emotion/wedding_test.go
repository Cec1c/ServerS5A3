package emotion

import (
	"context"
	"errors"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestCoupleEmotionRequiresFreshMarriedState(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	character := r.CharacterRecord{CharacterID: "19", AccountID: "a", Stats: map[string]int64{"emotion_index": 7}}
	if e := g.Character.Save(ctx, character); e != nil {
		t.Fatal(e)
	}
	owner, e := NewOwner(g)
	if e != nil {
		t.Fatal(e)
	}
	command := Command{AccountID: "a", SelectedCharacterID: 19, EmotionIndex: 1001}
	if _, e = owner.Apply(ctx, command); !errors.Is(e, ErrMarriageRequired) {
		t.Fatal(e)
	}
	character.Stats["wedding_v1_phase"], character.Stats["wedding_v1_partner"] = int64(wedding.Married), 20
	if e = r.SaveCharacterFields(ctx, g.Character, character, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
		t.Fatal(e)
	}
	if _, e = owner.Apply(ctx, command); e != nil {
		t.Fatal(e)
	}
	character, _, _ = g.Character.Load(ctx, "19")
	character.Stats["wedding_v1_phase"], character.Stats["wedding_v1_partner"] = 0, 0
	character.Stats["emotion_index"] = 0
	if e = r.SaveCharacterFields(ctx, g.Character, character, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
		t.Fatal(e)
	}
	if _, e = owner.Apply(ctx, command); !errors.Is(e, ErrMarriageRequired) {
		t.Fatal("stale owner restored couple emotion", e)
	}
	command.EmotionIndex = 7
	if _, e = owner.Apply(ctx, command); e != nil {
		t.Fatal("ordinary emotion affected", e)
	}
	character, _, _ = g.Character.Load(ctx, "19")
	if character.Stats["emotion_index"] != 7 {
		t.Fatal("ordinary emotion not saved")
	}
}
