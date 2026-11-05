package wedding

import (
	"context"
	"encoding/json"
	r "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// AllocateWeddingToken runs only with the GuestLifecycle's exclusive index
// scope. Native code compares this opaque u32; its value is server-owned and
// is never used as a character ID or reused by a later relationship/run.
func AllocateWeddingToken(ctx context.Context, settings r.SettingsRepository, now time.Time) (uint32, error) {
	record, index, e := loadGuestIndex(ctx, settings)
	if e != nil {
		return 0, e
	}
	if index.Next == 0xffffffff {
		return 0, ErrUnavailable
	}
	index.Next++
	if _, exists := index.Runs[index.Next]; exists {
		return 0, ErrState
	}
	raw, e := json.Marshal(index)
	if e != nil {
		return 0, e
	}
	record = r.CloneSettings(record)
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	record.Values["index"], record.UpdatedAt = string(raw), now
	if e = settings.Save(ctx, record); e != nil {
		return 0, e
	}
	return index.Next, nil
}

func (o Owner) withRelationshipTransaction(ctx context.Context, req PairRequest, apply func(Owner, r.Group) (PairResult, error)) (PairResult, error) {
	u, ok := o.Repositories.CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork)
	if !ok {
		return PairResult{}, ErrUnavailable
	}
	ids := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	scope, e := r.WeddingPairScope(ids)
	if e != nil {
		return PairResult{}, e
	}
	var result PairResult
	e = u.WithinWeddingGuestLifecycle(ctx, ids, nil, scope, func(g r.Group) error {
		inner := o
		inner.Repositories = g
		inner.Repositories.CharacterTrade = pairInTransaction{ids: [2]string{ids[0], ids[1]}, chars: g.Character, inventory: g.Inventory}
		var e error
		result, e = apply(inner, g)
		return e
	})
	if e != nil {
		return PairResult{}, e
	}
	return result, nil
}

// Existing installed relationships did not have a shared opaque token. This
// lazy metadata repair changes no relationship, wallet, quest or item assets.
func (o Owner) EnsureRelationshipToken(ctx context.Context, req PairRequest) (PairResult, error) {
	return o.withRelationshipTransaction(ctx, req, func(inner Owner, g r.Group) (PairResult, error) {
		return inner.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
			x, y, e := ValidatePair(*a, *b)
			if e != nil || x.Phase == Single {
				return ErrState
			}
			if x.RelationToken != 0 {
				return nil
			}
			token, e := AllocateWeddingToken(ctx, g.Settings, req.Now)
			if e != nil {
				return e
			}
			x.RelationToken, y.RelationToken = int64(token), int64(token)
			write(a, x)
			write(b, y)
			return nil
		})
	})
}
