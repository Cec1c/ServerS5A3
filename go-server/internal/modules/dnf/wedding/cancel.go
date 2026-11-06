package wedding

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// CancelEngagement is unilateral. The bridge verifies the requesting NPC and
// actor; the owner resolves and rechecks the real reciprocal pair, including
// an offline partner. Only the requesting actor pays the native PVF fee.
func (o Owner) CancelEngagement(ctx context.Context, req PairRequest) (PairResult, error) {
	u, ok := o.Repositories.CharacterTrade.(r.WeddingQuestUnitOfWork)
	if !ok || o.Rules == nil || o.Rules.EngagementCancellationGold <= 0 || req.Now.IsZero() || len(o.Rules.QuestIDs) != 2 || o.Rules.QuestIDs[0] != 20737 || o.Rules.QuestIDs[1] != 20738 {
		return PairResult{}, ErrUnavailable
	}
	if req.Actor.AccountID == "" || req.Partner.AccountID == "" || req.Actor.AccountID == req.Partner.AccountID {
		return PairResult{}, ErrOwnership
	}
	ids := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	if _, e := r.WeddingPairScope(ids); e != nil {
		return PairResult{}, ErrOwnership
	}
	ids, _ = r.CharacterInventoryGroupIDs(ids)
	var result PairResult
	err := u.WithinWeddingQuests(ctx, ids, func(chars r.CharacterRepository, quests r.QuestRepository) error {
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrState
		}
		rows := map[string]r.CharacterRecord{}
		for _, id := range ids {
			c, found, e := chars.Load(ctx, id)
			if e != nil {
				return e
			}
			if !found || c.CharacterID != id {
				return ErrOwnership
			}
			rows[id] = c
		}
		a, b := rows[req.Actor.CharacterID], rows[req.Partner.CharacterID]
		if a.AccountID != req.Actor.AccountID || b.AccountID != req.Partner.AccountID {
			return ErrOwnership
		}
		x, y, e := ValidatePair(a, b)
		if e != nil {
			return e
		}
		if x.Phase != Engaged {
			return ErrState
		}
		if a.Stats["gold"] < o.Rules.EngagementCancellationGold {
			return ErrGold
		}
		a.Stats["gold"] -= o.Rules.EngagementCancellationGold
		clear := func(s State) State {
			return State{Phase: Single, LovePoints: s.LovePoints, LastLoveDay: s.LastLoveDay, LastWeddingDay: s.LastWeddingDay, CooldownUntil: s.CooldownUntil}
		}
		write(&a, clear(x))
		write(&b, clear(y))
		rows[a.CharacterID], rows[b.CharacterID] = a, b
		for _, id := range ids {
			c := rows[id]
			c.UpdatedAt = req.Now
			rows[id] = c
			if e = r.SaveCharacterFields(ctx, chars, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
				return e
			}
			q, found, e := quests.Load(ctx, id)
			if e != nil {
				return e
			}
			if !found {
				continue
			}
			if q.CharacterID != id {
				return ErrOwnership
			}
			q = r.CloneQuest(q)
			changed := false
			for _, qid := range o.Rules.QuestIDs {
				if _, exists := q.States[qid]; exists {
					delete(q.States, qid)
					changed = true
				}
				if _, exists := q.Progress[qid]; exists {
					delete(q.Progress, qid)
					changed = true
				}
			}
			if changed {
				q.UpdatedAt = req.Now
				if e = r.SaveQuestFields(ctx, quests, q, r.QuestFieldStates, r.QuestFieldProgress); e != nil {
					return e
				}
			}
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrState
		}
		result = PairResult{Actor: rows[req.Actor.CharacterID], Partner: rows[req.Partner.CharacterID]}
		return nil
	})
	if err != nil {
		return PairResult{}, err
	}
	return result, nil
}
