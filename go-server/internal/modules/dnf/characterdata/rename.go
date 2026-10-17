package characterdata

import (
	"context"
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	RenameEntitlementStat        = "character_rename_entitlement_count"
	RenameLastUnixStat           = "character_rename_last_unix"
	RenameNextAllowedUnixStat    = "character_rename_next_allowed_unix"
	RenameMinimumLevelStat       = "character_rename_pending_min_level"
	RenameCooldownSecondsStat    = "character_rename_pending_cooldown_seconds"
	RenameReservationSecondsStat = "character_rename_pending_reservation_seconds"
	RenameCooldown               = 90 * 24 * time.Hour
	PreviousNameReservation      = 180 * 24 * time.Hour
)

var ErrRenameUnavailable = errors.New("character rename is unavailable")

type RenameRequest struct {
	AccountID   string
	CharacterID string
	Slot        int
	OldName     string
	NewName     string
	Now         time.Time
}

// Rename requires an already persisted entitlement. Retry after a committed
// reply failure acknowledges exactly the same old/new pair; it never spends a
// second entitlement or moves the cooldown/reservation deadline.
func Rename(ctx context.Context, repositories dnfrepo.Group, request RenameRequest) error {
	u, ok := repositories.CharacterCreate.(dnfrepo.CharacterNameMutationUnitOfWork)
	if !ok || request.AccountID == "" || request.CharacterID == "" || request.Now.IsZero() || request.Slot < 0 || request.Slot >= dnfrepo.DefaultCharacterSlotLimit || request.OldName == request.NewName {
		return ErrRenameUnavailable
	}
	if err := ValidateRenameName(request.NewName); err != nil {
		return err
	}
	return u.WithinCharacterNameMutation(ctx, request.CharacterID, repositories, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Settings == nil {
			return dnfrepo.ErrCharacterCreationTransactionUnavailable
		}
		character, found, err := tx.Character.Load(ctx, request.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != request.AccountID || character.Slot != request.Slot || character.Stats["delete_flag"] != 0 {
			return ErrRenameUnavailable
		}
		pending := character.Stats[RenameEntitlementStat]
		// Receipt is character-specific and participates in the same transaction.
		receiptScope := "character:" + request.CharacterID + ":rename"
		receipt, _, err := tx.Settings.Load(ctx, receiptScope)
		if err != nil {
			return err
		}
		if pending == 0 && character.Name == request.NewName && receipt.Values["old"] == request.OldName && receipt.Values["new"] == request.NewName {
			return nil
		}
		minimum := character.Stats[RenameMinimumLevelStat]
		cooldown := character.Stats[RenameCooldownSecondsStat]
		reservation := character.Stats[RenameReservationSecondsStat]
		if character.Name != request.OldName || minimum < 1 || character.Level < int(minimum) || pending != 1 || character.Stats[RenameNextAllowedUnixStat] > request.Now.Unix() ||
			(cooldown != 0 && cooldown != int64(RenameCooldown/time.Second)) || (reservation != 0 && reservation != int64(PreviousNameReservation/time.Second)) {
			return ErrRenameUnavailable
		}
		if err := CheckNameAvailable(ctx, tx.Character, tx.Settings, request.NewName, request.Now); err != nil {
			return err
		}
		if reservation > 0 {
			if err := ReservePreviousName(ctx, tx.Settings, character.Name, request.Now, request.Now.Add(time.Duration(reservation)*time.Second)); err != nil {
				return err
			}
		}
		receipt.Scope = receiptScope
		receipt.Values = map[string]string{"old": character.Name, "new": request.NewName}
		receipt.UpdatedAt = request.Now
		if err := tx.Settings.Save(ctx, receipt); err != nil {
			return err
		}
		character.Name = request.NewName
		character.Stats[RenameEntitlementStat] = 0
		character.Stats[RenameLastUnixStat] = request.Now.Unix()
		character.Stats[RenameNextAllowedUnixStat] = request.Now.Add(time.Duration(cooldown) * time.Second).Unix()
		character.UpdatedAt = request.Now
		return dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats)
	})
}
