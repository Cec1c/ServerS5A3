// Package carrygold owns purchases of the current client's gold-cap stages.
// It deliberately does not change existing reward, trade or auction policies.
package carrygold

import (
	"context"
	"errors"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	StageStat = "carry_gold_stage"
	MaxStage  = byte(4)
	MinLevel  = 60
	// The current EXE compares the wallet with 0x4C4B40 before opening the
	// dialog. Its DSTR 71470 and 72012 also promise this flat price.
	StageCost = int64(5_000_000)
)

var (
	ErrUnavailable = errors.New("carry gold transaction unavailable")
	ErrOwnership   = errors.New("carry gold character owner mismatch")
	ErrStage       = errors.New("carry gold stage is invalid or out of sequence")
	ErrLevel       = errors.New("carry gold upgrade requires level 60")
	ErrGold        = errors.New("carry gold upgrade requires more gold")
)

func Stage(character dnfrepo.CharacterRecord) (byte, error) {
	value := character.Stats[StageStat]
	if value < 0 || value > int64(MaxStage) {
		return 0, ErrStage
	}
	return byte(value), nil
}

type Result struct {
	Stage byte
	Gold  int64
	// Known is true only after loading and authorizing a valid durable stage.
	// It allows a stale client request to recover without charging again.
	Known bool
}

// Upgrade serializes the stage check, debit and write with the same character
// asset transaction used by other wallet mutations. No pre-transaction snapshot
// can overwrite a concurrent purchase or gold change.
func Upgrade(ctx context.Context, repos dnfrepo.Group, accountID, characterID string, requested byte) (Result, error) {
	if repos.CharacterAssets == nil {
		return Result{}, ErrUnavailable
	}
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" {
		return Result{}, ErrOwnership
	}
	var result Result
	err := repos.CharacterAssets.WithinCharacterAssets(ctx, characterID, func(characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return ErrOwnership
		}
		stage, err := Stage(character)
		if err != nil {
			return err
		}
		result = Result{Stage: stage, Gold: character.Stats["gold"], Known: true}
		if character.Level < MinLevel {
			return ErrLevel
		}
		if stage == MaxStage || requested != stage+1 {
			return ErrStage
		}
		if result.Gold < StageCost {
			return ErrGold
		}
		updated := dnfrepo.CloneCharacter(character)
		updated.Stats["gold"] = result.Gold - StageCost
		updated.Stats[StageStat] = int64(requested)
		updated.UpdatedAt = time.Now().UTC()
		return dnfrepo.SaveCharacterFields(ctx, characters, updated, dnfrepo.CharacterFieldStats)
	})
	if err != nil {
		return result, err
	}
	result.Stage, result.Gold = requested, result.Gold-StageCost
	return result, nil
}
