package quest

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

func pvpTrainingInitialTrigger(d Definition) (uint32, bool) {
	switch d.Path {
	case "n_quest/pvp/pvp_tookuk_03.qst", "n_quest/pvp/pvp_tookuk_04.qst", "n_quest/pvp/pvp_tookuk_05.qst":
	default:
		return 0, false
	}
	if normalizeQuestTag(d.Type) != "custom multi condition" || len(d.IntData) != 4 || d.IntData[3] < 0 || d.IntData[3] > 2 {
		return 0, false
	}
	for _, n := range d.IntData[:3] {
		if n < 0 || n > 511 {
			return 0, false
		}
	}
	v := packTriggerChannels(d.IntData[:3]...)
	return v, v != 0
}

// RepairUnprogressedPVPTraining 仅修复旧版初始化为1、从未收到条件推进的活动任务。
// 已完成、已经有条件上报或进度不符的记录都不重置，更不代完成或发奖励。
func (o *Owner) RepairUnprogressedPVPTraining(ctx context.Context, catalog *Catalog, accountID, characterID string, now time.Time) (repo.QuestRecord, bool, error) {
	if o == nil || catalog == nil || o.repositories.CharacterSettlement == nil || accountID == "" || characterID == "" || now.IsZero() {
		return repo.QuestRecord{}, false, ErrOwnerUnavailable
	}
	var out repo.QuestRecord
	changed := false
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return ErrOwnerUnavailable
		}
		c, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || c.AccountID != accountID {
			return ErrCharacterNotFound
		}
		record, found, err := tx.Quest.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if record.CharacterID != characterID {
			return fmt.Errorf("修炼任务归属不符")
		}
		out = repo.CloneQuest(record)
		var fields []repo.QuestField
		seen := map[int64]bool{}
		for _, group := range []struct {
			states map[int64]repo.QuestState
			field  repo.QuestField
		}{{out.States, repo.QuestFieldStates}, {out.Progress, repo.QuestFieldProgress}} {
			dirty := false
			for id, state := range group.states {
				if seen[id] {
					continue
				}
				seen[id] = true
				d, exists := catalog.Find(id)
				want, valid := pvpTrainingInitialTrigger(d)
				if !exists || !valid || want <= 1 || !isActiveQuestStatus(state.Status) || state.ProgressValue != 1 || state.TriggerType != 0 || state.Extra["pvf_path"] != d.Path || state.Extra["pvp_training_initial_trigger_version"] != "" {
					continue
				}
				if _, observed := state.Extra["last_set_trigger_type"]; observed {
					continue
				}
				state.ProgressValue = int64(want)
				state.UpdatedAt = now
				if state.Extra == nil {
					state.Extra = map[string]string{}
				}
				state.Extra["pvp_training_initial_trigger_version"] = "1"
				group.states[id] = state
				dirty = true
				changed = true
			}
			if dirty {
				fields = append(fields, group.field)
			}
		}
		if changed {
			out.UpdatedAt = now
			return repo.SaveQuestFields(ctx, tx.Quest, out, fields...)
		}
		return nil
	})
	if err != nil {
		return repo.QuestRecord{}, false, err
	}
	return out, changed, nil
}
