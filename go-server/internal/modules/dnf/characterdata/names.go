package characterdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// NameReservationKey treats case, width and diacritics alike, as the MySQL
// utf8mb4_unicode_ci character-name index does. The shared namespace transaction
// serializes both creation and rename; reservations apply to all accounts.
func NameReservationKey(name string) string {
	var buffer collate.Buffer
	key := collate.New(language.Und, collate.IgnoreCase, collate.IgnoreDiacritics, collate.IgnoreWidth).KeyFromString(&buffer, strings.TrimSpace(name))
	digest := sha256.Sum256(key)
	return "reserved:" + hex.EncodeToString(digest[:])
}

func CheckNameAvailable(ctx context.Context, characters dnfrepo.CharacterRepository, settings dnfrepo.SettingsRepository, name string, now time.Time) error {
	if characters == nil || settings == nil || strings.TrimSpace(name) == "" {
		return dnfrepo.ErrCharacterCreationTransactionUnavailable
	}
	if _, exists, err := characters.FindIDByName(ctx, name); err != nil {
		return err
	} else if exists {
		return dnfrepo.ErrCharacterNameExists
	}
	state, _, err := settings.Load(ctx, dnfrepo.CharacterNameMutationScope)
	if err != nil {
		return err
	}
	value := state.Values[NameReservationKey(name)]
	if value == "" {
		return nil
	}
	deadline, err := strconv.ParseInt(value, 10, 64)
	// A malformed reservation is not permission to claim somebody else's name.
	if err != nil || deadline > now.Unix() {
		return dnfrepo.ErrCharacterNameExists
	}
	return nil
}

func ReservePreviousName(ctx context.Context, settings dnfrepo.SettingsRepository, name string, now time.Time, until time.Time) error {
	if settings == nil {
		return dnfrepo.ErrCharacterCreationTransactionUnavailable
	}
	state, _, err := settings.Load(ctx, dnfrepo.CharacterNameMutationScope)
	if err != nil {
		return err
	}
	state.Scope = dnfrepo.CharacterNameMutationScope
	if state.Values == nil {
		state.Values = make(map[string]string)
	}
	for key, value := range state.Values {
		if !strings.HasPrefix(key, "reserved:") {
			continue
		}
		if deadline, err := strconv.ParseInt(value, 10, 64); err == nil && deadline <= now.Unix() {
			delete(state.Values, key)
		}
	}
	state.Values[NameReservationKey(name)] = strconv.FormatInt(until.Unix(), 10)
	state.UpdatedAt = now
	return settings.Save(ctx, state)
}
