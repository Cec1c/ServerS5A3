package emotion

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

var (
	ErrOwnerUnavailable  = errors.New("emotion owner unavailable")
	ErrCharacterRequired = errors.New("selected character id required")
	ErrCharacterNotFound = errors.New("character record not found")
	ErrAccountMismatch   = errors.New("character account does not match session account")
	ErrMarriageRequired  = errors.New("couple emotion requires a married character")
)

const statKey = "emotion_index"

// Command contains the transport-independent identity and emotion selected by
// the current character.
type Command struct {
	AccountID           string
	SelectedCharacterID uint16
	EmotionIndex        uint16
	UpdatedAt           time.Time
}

type Result struct {
	CharacterID  string
	EmotionIndex uint16
}

// Owner owns the durable emotion state. Packet decoding and acknowledgements
// remain in dnfbridge.
type Owner struct {
	characters dnfrepo.CharacterRepository
	settlement dnfrepo.CharacterSettlementUnitOfWork
}

func NewOwner(repos dnfrepo.Group) (*Owner, error) {
	if repos.Character == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{characters: repos.Character, settlement: repos.CharacterSettlement}, nil
}

func (o *Owner) Apply(ctx context.Context, cmd Command) (Result, error) {
	if o == nil || o.characters == nil {
		return Result{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return Result{}, ErrCharacterRequired
	}
	if cmd.EmotionIndex == 1001 {
		if o.settlement == nil {
			return Result{}, ErrOwnerUnavailable
		}
		var result Result
		err := o.settlement.WithinCharacterSettlement(ctx, strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10), func(g dnfrepo.Group) error {
			if g.Character == nil {
				return ErrOwnerUnavailable
			}
			var err error
			result, err = o.applyToCharacter(ctx, g.Character, cmd, true)
			return err
		})
		if err != nil {
			return Result{}, err
		}
		return result, nil
	}
	return o.applyToCharacter(ctx, o.characters, cmd, false)
}

func (o *Owner) applyToCharacter(ctx context.Context, characters dnfrepo.CharacterRepository, cmd Command, married bool) (Result, error) {
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return Result{}, err
	}
	if !found || strings.TrimSpace(character.CharacterID) != characterID {
		return Result{}, fmt.Errorf("%w: character=%s", ErrCharacterNotFound, characterID)
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" &&
		strings.TrimSpace(character.AccountID) != accountID {
		return Result{}, fmt.Errorf("%w: character=%s", ErrAccountMismatch, characterID)
	}
	if married {
		state, err := wedding.Read(character)
		if err != nil || state.Phase != wedding.Married {
			return Result{}, ErrMarriageRequired
		}
	}

	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		character.Stats = make(map[string]int64, 1)
	}
	character.Stats[statKey] = int64(cmd.EmotionIndex)
	character.UpdatedAt = commandTime(cmd.UpdatedAt)
	if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
		return Result{}, err
	}
	return Result{CharacterID: characterID, EmotionIndex: cmd.EmotionIndex}, nil
}

func commandTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
