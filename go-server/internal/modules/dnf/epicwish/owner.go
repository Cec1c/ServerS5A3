// Package epicwish owns the character's five durable epic wish selections.
package epicwish

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const SlotCount = 5

var ErrUnavailable = errors.New("epic wish repository unavailable")
var ErrInvalid = errors.New("epic wish selection invalid")
var ErrCharacter = errors.New("epic wish character ownership mismatch")

// Slots retain empty positions: the native UI sends all five slots, including
// zero/-1 empty entries. Compacting them would move the player's selections.
type Slots [SlotCount]int32

func key(slot int) string { return fmt.Sprintf("epic_wish_item_%d", slot) }

func Read(character dnfrepo.CharacterRecord) Slots {
	var slots Slots
	for i := range slots {
		value := character.Stats[key(i)]
		if value > 0 && value <= 0x7fffffff {
			slots[i] = int32(value)
		}
	}
	return slots
}

// Save updates only these preference keys inside the character settlement.
// It never touches inventory/equipment and does not grant wished-for items.
func Save(ctx context.Context, repos dnfrepo.Group, accountID, characterID string, slots Slots, allowed func(int32) bool) error {
	if repos.Character == nil || repos.CharacterSettlement == nil || allowed == nil {
		return ErrUnavailable
	}
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" {
		return ErrCharacter
	}
	seen := make(map[int32]bool)
	for i, id := range slots {
		if id == -1 {
			slots[i] = 0
			continue
		}
		if id == 0 {
			continue
		}
		if id < 0 || seen[id] || !allowed(id) {
			return ErrInvalid
		}
		seen[id] = true
	}
	return repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil {
			return ErrUnavailable
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return ErrCharacter
		}
		character = dnfrepo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		for i, id := range slots {
			character.Stats[key(i)] = int64(id)
		}
		character.UpdatedAt = time.Now().UTC()
		return dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldStats)
	})
}
