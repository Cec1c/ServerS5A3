package wedding

import (
	"context"
	"errors"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrCapsuleEnergy       = errors.New("wedding capsule energy insufficient")
	ErrCapsuleOwnLimit     = errors.New("wedding capsule own stock full")
	ErrCapsuleEmpty        = errors.New("wedding capsule has no stock to send")
	ErrCapsulePartnerLimit = errors.New("wedding capsule partner stock full")
)

type CapsuleState struct{ Progress, Own, Taken int64 }
type CapsuleCommand struct {
	Opcode  uint16
	Receipt ExtensionReceipt
	Grant   AssetGrant
}
type CapsuleResult struct {
	PairResult
	States   [2]CapsuleState
	Granted  int64
	Replayed bool
}

func ReadCapsule(c r.CharacterRecord, rules CapsuleRules) (CapsuleState, error) {
	if !rules.valid() {
		return CapsuleState{}, ErrUnavailable
	}
	w, e := Read(c)
	if e != nil {
		return CapsuleState{}, e
	}
	if w.Phase != Married || c.Stats[statPrefix+"capsule_partner"] != w.PartnerID || c.Stats[statPrefix+"capsule_married_at"] != w.MarriedAt {
		return CapsuleState{}, nil
	}
	s := CapsuleState{c.Stats[statPrefix+"capsule_progress"], c.Stats[statPrefix+"capsule_own"], c.Stats[statPrefix+"capsule_taken"]}
	if s.Progress < 0 || s.Progress > rules.MaximumGauge || s.Own < 0 || s.Own > rules.MaximumOwn || s.Taken < 0 || s.Taken > rules.MaximumTaken {
		return CapsuleState{}, ErrState
	}
	return s, nil
}
func storeCapsule(c *r.CharacterRecord, s CapsuleState) error {
	w, e := Read(*c)
	if e != nil || w.Phase != Married {
		return ErrState
	}
	c.Stats[statPrefix+"capsule_partner"], c.Stats[statPrefix+"capsule_married_at"] = w.PartnerID, w.MarriedAt
	c.Stats[statPrefix+"capsule_progress"], c.Stats[statPrefix+"capsule_own"], c.Stats[statPrefix+"capsule_taken"] = s.Progress, s.Own, s.Taken
	return nil
}

// The caller must invoke this only in the already receipted, server-accepted
// recommended dungeon settlement and save the Wedding field in that same UOW.
// No item is granted here; one full gauge is converted by the real 1052 request.
func AccumulateCapsuleClear(c *r.CharacterRecord, rules CapsuleRules) error {
	if c == nil || !rules.valid() {
		return ErrUnavailable
	}
	w, e := Read(*c)
	if e != nil {
		return e
	}
	if w.Phase != Married {
		return nil
	}
	s, e := ReadCapsule(*c, rules)
	if e != nil {
		return e
	}
	s.Progress = min(rules.MaximumGauge, s.Progress+rules.Gain)
	return storeCapsule(c, s)
}

func (o Owner) MutateCapsule(ctx context.Context, req PairRequest, rules CapsuleRules, cmd CapsuleCommand) (CapsuleResult, error) {
	if !rules.valid() || cmd.Opcode < 1052 || cmd.Opcode > 1054 || cmd.Receipt.Opcode != cmd.Opcode {
		return CapsuleResult{}, ErrState
	}
	var states [2]CapsuleState
	var granted int64
	pair, replayed, e := o.extensionMutation(ctx, req, cmd.Receipt, func(a, b *r.CharacterRecord, ai, bi *r.InventoryRecord, _ *Room, _ r.Group) error {
		var e error
		states[0], e = ReadCapsule(*a, rules)
		if e != nil {
			return e
		}
		states[1], e = ReadCapsule(*b, rules)
		if e != nil {
			return e
		}
		x, y := &states[0], &states[1]
		switch cmd.Opcode {
		case 1052:
			if x.Progress < rules.MaximumGauge {
				return errors.Join(ErrItem, ErrCapsuleEnergy)
			}
			if x.Own >= rules.MaximumOwn {
				return errors.Join(ErrItem, ErrCapsuleOwnLimit)
			}
			x.Progress -= rules.MaximumGauge
			x.Own++
		case 1053:
			if x.Own <= 0 {
				return errors.Join(ErrItem, ErrCapsuleEmpty)
			}
			if x.Own > rules.MaximumTaken-y.Taken {
				return errors.Join(ErrItem, ErrCapsulePartnerLimit)
			}
			y.Taken += x.Own
			x.Own = 0
		case 1054:
			if x.Taken <= 0 || cmd.Grant == nil {
				return ErrItem
			}
			if e = cmd.Grant(ai, rules.ItemID, x.Taken, req.Now); e != nil {
				return e
			}
			granted = x.Taken
			x.Taken = 0
		}
		if e = storeCapsule(a, *x); e != nil {
			return e
		}
		return storeCapsule(b, *y)
	})
	if e != nil {
		return CapsuleResult{}, e
	}
	if replayed {
		states[0], e = ReadCapsule(pair.Actor, rules)
		if e != nil {
			return CapsuleResult{}, e
		}
		states[1], e = ReadCapsule(pair.Partner, rules)
		if e != nil {
			return CapsuleResult{}, e
		}
	}
	return CapsuleResult{PairResult: pair, States: states, Granted: granted, Replayed: replayed}, nil
}
