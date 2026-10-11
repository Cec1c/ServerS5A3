package quest

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Supplied only after an owned normal Anton phase succeeds, never from op33.
// ResetHour is the deployment's existing service-day policy, not a PVF claim.
type AntonPrerequisiteClear struct {
	CharacterID, AccountID, InstanceID string
	Phase                              byte
	CompletedAt                        time.Time
	ResetHour                          int
}

type AntonPrerequisiteProgress struct {
	QuestID       int64
	ProgressValue int64
	Changed       bool
}

// Scope deliberately stays on the actual quest named by Luke's original
// admission rule. Other raid-quest dialects need their own asset evidence.
func (c *Catalog) planAntonPrerequisiteClear(record dnfrepo.QuestRecord, questID int64, input AntonPrerequisiteClear) (dnfrepo.QuestRecord, dnfrepo.QuestField, AntonPrerequisiteProgress, error) {
	result := AntonPrerequisiteProgress{QuestID: questID}
	id, err := hex.DecodeString(input.InstanceID)
	if err != nil || len(id) != 16 || input.CharacterID == "" || input.AccountID == "" || record.CharacterID != input.CharacterID || input.Phase > 1 || input.CompletedAt.IsZero() || input.ResetHour < 0 || input.ResetHour > 23 {
		return record, "", result, fmt.Errorf("Anton prerequisite clear identity or clock invalid")
	}
	d, found := c.Find(questID)
	if !found || normalizeQuestTag(d.Type) != "raid phase clear" || d.SubType != 0 || len(d.IntData) != 8 {
		return record, "", result, fmt.Errorf("original Anton prerequisite definition unavailable")
	}
	for phase := 0; phase < 2; phase++ {
		offset := phase * 4
		if d.IntData[offset] != int64(phase) || d.IntData[offset+1] != 0 || d.IntData[offset+2] < 1 || d.IntData[offset+2] > 511 || d.IntData[offset+3] != -1 {
			return record, "", result, fmt.Errorf("original Anton prerequisite counter dialect unavailable")
		}
	}
	next := dnfrepo.CloneQuest(record)
	state, field, active := mutableQuestState(&next, questID)
	if !active || !isActiveQuestStatus(state.Status) {
		return record, "", result, nil
	}
	result.ProgressValue = state.ProgressValue
	if state.ProgressValue < 0 || state.ProgressValue > (1<<18)-1 {
		return record, "", result, fmt.Errorf("Anton prerequisite packed progress invalid")
	}
	// Reject later activations/updates: a delayed old result must not credit a
	// newly accepted quest or roll back a newer authoritative record.
	if state.UpdatedAt.After(input.CompletedAt) {
		return record, "", result, nil
	}
	for phase := 0; phase < 2; phase++ {
		if (uint32(state.ProgressValue)>>uint(phase*9))&511 > uint32(d.IntData[phase*4+2]) {
			return record, "", result, fmt.Errorf("Anton prerequisite remaining counter exceeds actual requirement")
		}
	}
	local := input.CompletedAt.In(time.FixedZone("DNF service UTC+8", 8*60*60))
	day := time.Date(local.Year(), local.Month(), local.Day(), input.ResetHour, 0, 0, 0, local.Location())
	if local.Before(day) {
		day = day.AddDate(0, 0, -1)
	}
	suffix := strconv.Itoa(int(input.Phase))
	dayKey, instanceKey := "anton_prerequisite_day_v1_"+suffix, "anton_prerequisite_instance_v1_"+suffix
	if stored := state.Extra[dayKey]; stored != "" {
		previous, err := strconv.ParseInt(stored, 10, 64)
		if err != nil || previous <= 0 || previous > day.Unix() || state.Extra[instanceKey] == "" {
			return record, "", result, fmt.Errorf("Anton prerequisite daily receipt inconsistent")
		}
		if previous == day.Unix() || state.Extra[instanceKey] == input.InstanceID {
			return record, "", result, nil
		}
	} else if state.Extra[instanceKey] != "" {
		return record, "", result, fmt.Errorf("Anton prerequisite daily receipt missing")
	}
	shift := uint(input.Phase * 9)
	if (uint32(state.ProgressValue)>>shift)&511 == 0 {
		return record, "", result, nil
	}
	state.ProgressValue -= int64(1) << shift
	state.UpdatedAt = input.CompletedAt
	if state.Extra == nil {
		state.Extra = make(map[string]string)
	}
	state.Extra[dayKey], state.Extra[instanceKey] = strconv.FormatInt(day.Unix(), 10), input.InstanceID
	// Reaching zero only enables normal quest submission. No completed flag,
	// selected reward or inventory mutation happens in this owner.
	if field == dnfrepo.QuestFieldStates {
		next.States[questID] = state
	} else {
		next.Progress[questID] = state
	}
	next.UpdatedAt = input.CompletedAt
	result.ProgressValue, result.Changed = state.ProgressValue, true
	return next, field, result, nil
}

func (o *Owner) ApplyAntonPrerequisitePhaseClear(ctx context.Context, catalog *Catalog, questID int64, input AntonPrerequisiteClear) (AntonPrerequisiteProgress, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return AntonPrerequisiteProgress{}, ErrOwnerUnavailable
	}
	result := AntonPrerequisiteProgress{QuestID: questID}
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, input.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return ErrOwnerUnavailable
		}
		character, found, err := tx.Character.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != input.CharacterID || strings.TrimSpace(character.AccountID) != input.AccountID {
			return fmt.Errorf("Anton prerequisite frozen account mismatch")
		}
		record, found, err := tx.Quest.Load(ctx, input.CharacterID)
		if err != nil || !found {
			return err
		}
		next, field, planned, err := catalog.planAntonPrerequisiteClear(record, questID, input)
		if err != nil {
			return err
		}
		if planned.Changed {
			if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, next, field); err != nil {
				return err
			}
		}
		result = planned
		return nil
	})
	if err != nil {
		return AntonPrerequisiteProgress{}, err
	}
	return result, nil
}
