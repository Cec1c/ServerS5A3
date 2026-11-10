package wedding

import (
	"context"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// RecoverGuestChurchDeparture only retires a proven obsolete Church visit.
// A transient read failure or malformed/missing run is not a cancellation.
// The immutable index/ledger and the guest location are read under the same
// lifecycle locks used by cancellation, settlement and guest entry.
func (o Owner) RecoverGuestChurchDeparture(ctx context.Context, actor Actor, expected r.CharacterRecord, now time.Time, destination func(r.CharacterRecord) (CeremonyReturnLocation, error), revalidate func() bool) (r.CharacterRecord, bool, error) {
	if actor.AccountID == "" || actor.CharacterID != expected.CharacterID || actor.AccountID != expected.AccountID || destination == nil || revalidate == nil || now.IsZero() || o.Repositories.Settings == nil {
		return r.CharacterRecord{}, false, ErrOwnership
	}
	key, at := expected.Stats[statPrefix+"guest_key"], expected.Stats[statPrefix+"guest_at"]
	if key <= 0 || key > 0xffffffff || at <= 0 || expected.Stats["town_id"] != 39 || expected.Stats["area_id"] < 8 || expected.Stats["area_id"] > 38 {
		return r.CharacterRecord{}, false, ErrState
	}
	_, index, err := loadGuestIndex(ctx, o.Repositories.Settings)
	if err != nil {
		return r.CharacterRecord{}, false, err
	}
	ref, found := index.Runs[uint32(key)]
	if !found || ref.At != at || actor.CharacterID == ref.Hosts[0] || actor.CharacterID == ref.Hosts[1] {
		return r.CharacterRecord{}, false, ErrState
	}
	var result r.CharacterRecord
	moved := false
	err = o.guestMutation(ctx, ref.Hosts[:], []string{actor.CharacterID}, now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		guest := chars[actor.CharacterID]
		if guest.AccountID != actor.AccountID || guest.Slot < 0 || guest.Stats["delete_flag"] != 0 || !revalidate() {
			return ErrOwnership
		}
		result = guest
		// A later entry or move already won: never clear its newer visit.
		if guest.Stats[statPrefix+"guest_key"] != key || guest.Stats[statPrefix+"guest_at"] != at || guest.Stats["town_id"] != expected.Stats["town_id"] || guest.Stats["area_id"] != expected.Stats["area_id"] {
			return nil
		}
		_, currentIndex, err := loadGuestIndex(ctx, g.Settings)
		if err != nil {
			return err
		}
		currentRef, found := currentIndex.Runs[uint32(key)]
		if !found || currentRef.Hosts != ref.Hosts || currentRef.At != ref.At {
			return ErrState
		}
		ledger, found, err := decodeGuestLedger(record, at)
		if err != nil {
			return err
		}
		if !found || ledger.Key != uint32(key) || ledger.Hosts != ref.Hosts || int64(ledger.Area) != guest.Stats["area_id"] {
			return ErrState
		}
		_, invited := ledger.Invitations[actor.CharacterID]
		obsolete := ledger.Settled || ledger.Refunded || !invited
		if !obsolete {
			// A closed index without a terminal ledger is inconsistent, not a
			// license to discard the visit after a failed repository read.
			if currentRef.Closed {
				return ErrState
			}
			first, err := Read(chars[ref.Hosts[0]])
			if err != nil {
				return err
			}
			second, err := Read(chars[ref.Hosts[1]])
			if err != nil {
				return err
			}
			stillOwnsRun := func(s State) bool {
				return s.CeremonyAt == at && (s.Phase == Reserved || s.Phase == InCeremony || s.Phase == Married)
			}
			if !stillOwnsRun(first) && !stillOwnsRun(second) {
				obsolete = true
			} else {
				state, _, err := ValidatePair(chars[ref.Hosts[0]], chars[ref.Hosts[1]])
				if err != nil {
					return err
				}
				obsolete = ReservationExpired(state, now.Unix())
			}
		}
		if !obsolete {
			return nil
		}
		landing, err := destination(guest)
		if err != nil {
			return err
		}
		if !landing.Valid() || guest.Stats["area_state"] < 0 || guest.Stats["area_state"] > 255 {
			return ErrState
		}
		if _, found := guest.Stats["area_state"]; !found {
			return ErrState
		}
		guest = r.CloneCharacter(guest)
		guest.Stats[statPrefix+"guest_key"], guest.Stats[statPrefix+"guest_at"] = 0, 0
		guest.Stats["town_id"], guest.Stats["area_id"] = landing.Town, landing.Area
		guest.Stats["pos_x"], guest.Stats["pos_y"], guest.Stats["direction"] = landing.X, landing.Y, landing.Direction
		guest.UpdatedAt = now
		if err := r.SaveCharacterFields(ctx, g.Character, guest, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
			return err
		}
		if !revalidate() {
			return ErrOwnership
		}
		result, moved = guest, true
		return nil
	})
	if err != nil {
		return r.CharacterRecord{}, false, err
	}
	return result, moved, nil
}
