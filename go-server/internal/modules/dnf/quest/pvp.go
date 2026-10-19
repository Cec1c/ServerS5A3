package quest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func isPVPWinDefinition(definition Definition) bool {
	return normalizeQuestTag(definition.Type) == "pvp quest" && definition.SubType == 1 &&
		len(definition.IntData) == 2 && definition.IntData[0] > 0 &&
		definition.IntData[0] <= math.MaxUint32 && definition.IntData[1] == 0
}

type PVPProgressResult struct {
	Quest            dnfrepo.QuestRecord
	SnapshotRequired bool
}

// ApplyPVPWin consumes one authoritative ranked 1v1 win. The existing quest
// finish transaction remains responsible for granting the quest reward.
func (o *Owner) ApplyPVPWin(ctx context.Context, catalog *Catalog, accountID, characterID, completionKey string, completedAt time.Time) (PVPProgressResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return PVPProgressResult{}, ErrOwnerUnavailable
	}
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(characterID) == "" ||
		strings.TrimSpace(completionKey) == "" || completedAt.IsZero() {
		return PVPProgressResult{}, fmt.Errorf("PVP quest completion identity/time missing")
	}
	digest := sha256.Sum256([]byte(completionKey))
	var result PVPProgressResult
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return ErrOwnerUnavailable
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return ErrCharacterNotFound
		}
		record, found, err := tx.Quest.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if found && record.CharacterID != characterID {
			return fmt.Errorf("PVP quest character mismatch")
		}
		result.Quest = dnfrepo.CloneQuest(record)
		result.SnapshotRequired = found
		replayed := true
		for i := 0; i < 4; i++ {
			v, exists := character.Stats[fmt.Sprintf("pvp_quest_receipt_%d", i)]
			replayed = replayed && exists && v == int64(binary.LittleEndian.Uint64(digest[i*8:]))
		}
		if replayed {
			return nil
		}
		var fields []dnfrepo.QuestField
		seen := make(map[int64]bool)
		visit := func(states map[int64]dnfrepo.QuestState, field dnfrepo.QuestField) {
			changed := false
			for id, state := range states {
				if seen[id] {
					continue
				}
				seen[id] = true
				definition, exists := catalog.Find(id)
				if !exists || !isPVPWinDefinition(definition) || !isActiveQuestStatus(state.Status) ||
					state.ProgressValue <= 0 || state.ProgressValue > definition.IntData[0] || state.UpdatedAt.After(completedAt) {
					continue
				}
				state.ProgressValue--
				state.UpdatedAt = completedAt
				if state.Extra == nil {
					state.Extra = make(map[string]string)
				}
				state.Extra["completion_kind"] = "pvp_win"
				state.Extra["completion_key"] = completionKey
				state.Extra["pvf_path"] = definition.Path
				if state.ProgressValue == 0 {
					state.Extra["reward_state"] = "pending"
				}
				states[id] = state
				changed = true
			}
			if changed {
				fields = append(fields, field)
			}
		}
		visit(result.Quest.States, dnfrepo.QuestFieldStates)
		visit(result.Quest.Progress, dnfrepo.QuestFieldProgress)
		if len(fields) > 0 {
			result.Quest.UpdatedAt = completedAt
			if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, result.Quest, fields...); err != nil {
				return err
			}
		}
		character = dnfrepo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		for i := 0; i < 4; i++ {
			character.Stats[fmt.Sprintf("pvp_quest_receipt_%d", i)] = int64(binary.LittleEndian.Uint64(digest[i*8:]))
		}
		return dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldStats)
	})
	if err != nil {
		return PVPProgressResult{}, err
	}
	return result, nil
}
