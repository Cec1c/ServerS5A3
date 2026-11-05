package wedding

import (
	"context"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// Divorce accepts only a persisted Married pair. The bridge owns unilateral
// NPC authorization (6), or the exact bound spouse's current consent (7).
// Local policy follows the native alimony table: 6 charges the initiator's
// second entry, 7 charges both spouses the first entry. No inventory refunds
// are minted; all relationship privileges and pair-scoped room data retire.
func (o Owner) Divorce(ctx context.Context, req PairRequest, rules *ExtensionRules, operation byte) (PairResult, error) {
	if rules == nil || (operation != 6 && operation != 7) || rules.DivorceDays < 0 || rules.DivorceDays > 36500 || rules.DivorceGold[0] < 0 || rules.DivorceGold[1] < 0 || req.Now.Unix() < 0 || req.Now.Unix() > 0xffffffff || rules.DivorceDays > (0xffffffff-req.Now.Unix())/86400 {
		return PairResult{}, ErrUnavailable
	}
	result, _, e := o.extensionMutation(ctx, req, ExtensionReceipt{}, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord, room *Room, g r.Group) error {
		scope, e := r.WeddingPairScope([]string{a.CharacterID, b.CharacterID})
		if e != nil {
			return e
		}
		settings, _, e := g.Settings.Load(ctx, scope)
		if e != nil {
			return e
		}
		debt, e := GuestLedgerHasDebt(settings)
		if e != nil {
			return e
		}
		if debt {
			return ErrState
		}
		x, y, e := ValidatePair(*a, *b)
		if e != nil || x.Phase != Married {
			return ErrState
		}
		cost := rules.DivorceGold[0]
		if operation == 6 {
			cost = rules.DivorceGold[1]
		}
		if a.Stats["gold"] < cost || operation == 7 && b.Stats["gold"] < cost {
			return ErrGold
		}
		a.Stats["gold"] -= cost
		if operation == 7 {
			b.Stats["gold"] -= cost
		}
		cooldown := req.Now.Unix() + rules.DivorceDays*86400
		for i, c := range []*r.CharacterRecord{a, b} {
			old := []State{x, y}[i]
			// The native confirmation explicitly withdraws love points and the
			// house. Preserve personal daily watermarks, never old pair stock.
			for key := range c.Stats {
				if strings.HasPrefix(key, statPrefix) {
					delete(c.Stats, key)
				}
			}
			write(c, State{Phase: Single, CooldownUntil: max(old.CooldownUntil, cooldown), LastWeddingDay: old.LastWeddingDay, LastLoveDay: old.LastLoveDay})
			if c.Stats["mood_value"] == 1001 {
				c.Stats["mood_value"] = 0
			}
			if c.Stats["emotion_index"] == 1001 {
				c.Stats["emotion_index"] = 0
			}
			q, found, e := g.Quest.Load(ctx, c.CharacterID)
			if e != nil {
				return e
			}
			if found {
				if q.CharacterID != c.CharacterID {
					return ErrOwnership
				}
				q = r.CloneQuest(q)
				for _, id := range []int64{20737, 20738} {
					delete(q.States, id)
					delete(q.Progress, id)
				}
				q.UpdatedAt = req.Now
				if e = r.SaveQuestFields(ctx, g.Quest, q, r.QuestFieldStates, r.QuestFieldProgress); e != nil {
					return e
				}
			}
		}
		*room = Room{Version: 1, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
		return nil
	})
	return result, e
}
