// Package characterfriend owns durable, directed character friend relations.
// Native packet layouts and live session availability belong to dnfbridge.
package characterfriend

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrUnavailable      = errors.New("character friend repository is unavailable")
	ErrInvalidCharacter = errors.New("character friend identity is invalid")
	ErrAccountMissing   = errors.New("character friend account is missing")
	ErrSourceNotOwned   = errors.New("character friend source is not owned by the account")
	ErrTargetMissing    = errors.New("character friend target is missing")
)

const metadataPrefix = "character_friend_v1:"

// Shipped current EXE 029C2D7C rejects additions at 0x40 rows.
const MaximumFriends = 64

type Owner struct{ repositories dnfrepo.Group }

func NewOwner(repositories dnfrepo.Group) *Owner { return &Owner{repositories: repositories} }

// Add is one-way. It does not fabricate the reciprocal relation or create an
// account-friend invitation. The target must be an existing named character.
func (o *Owner) Add(ctx context.Context, accountID, sourceID, targetID string) error {
	ctx = contextOrBackground(ctx)
	if !validID(targetID) || sourceID == targetID {
		return ErrInvalidCharacter
	}
	account, err := o.loadSource(ctx, accountID, sourceID)
	if err != nil {
		return err
	}
	writer, ok := o.repositories.Account.(dnfrepo.AccountMetadataLimitedEntryWriter)
	if !ok {
		return ErrUnavailable
	}
	target, found, err := o.repositories.Character.Load(ctx, targetID)
	if err != nil {
		return err
	}
	if !found || target.CharacterID != targetID || strings.TrimSpace(target.Name) == "" || strings.TrimSpace(target.AccountID) == "" {
		return ErrTargetMissing
	}
	return writer.SaveMetadataEntryWithPrefixLimit(ctx, account.AccountID, metadataPrefix+sourceID+":", metadataKey(sourceID, targetID), "1", MaximumFriends, time.Now().UTC())
}

// Remove writes a tombstone for just this relation; it never saves a stale
// whole-account metadata map. It also allows removal after target deletion.
func (o *Owner) Remove(ctx context.Context, accountID, sourceID, targetID string) error {
	ctx = contextOrBackground(ctx)
	if !validID(targetID) || sourceID == targetID {
		return ErrInvalidCharacter
	}
	account, err := o.loadSource(ctx, accountID, sourceID)
	if err != nil {
		return err
	}
	writer, ok := o.repositories.Account.(dnfrepo.AccountMetadataEntryWriter)
	if !ok {
		return ErrUnavailable
	}
	return writer.SaveMetadataEntry(ctx, account.AccountID, metadataKey(sourceID, targetID), "0", time.Now().UTC())
}

// List resolves current repository records on every read, so rename and level
// changes cannot be hidden behind a saved name/level cache. Deleted targets and
// malformed/inactive metadata are excluded without rewriting account state.
func (o *Owner) List(ctx context.Context, accountID, sourceID string) ([]dnfrepo.CharacterRecord, error) {
	ctx = contextOrBackground(ctx)
	account, err := o.loadSource(ctx, accountID, sourceID)
	if err != nil {
		return nil, err
	}
	prefix := metadataPrefix + sourceID + ":"
	ids := make([]string, 0)
	for key, value := range account.Metadata {
		if value != "1" || !strings.HasPrefix(key, prefix) {
			continue
		}
		id := strings.TrimPrefix(key, prefix)
		if validID(id) && id != sourceID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.ParseUint(ids[i], 10, 16)
		b, _ := strconv.ParseUint(ids[j], 10, 16)
		return a < b
	})
	rows := make([]dnfrepo.CharacterRecord, 0, len(ids))
	for _, id := range ids {
		row, found, err := o.repositories.Character.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if found && row.CharacterID == id && strings.TrimSpace(row.Name) != "" && strings.TrimSpace(row.AccountID) != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (o *Owner) loadSource(ctx context.Context, accountID, sourceID string) (dnfrepo.AccountRecord, error) {
	if err := ctx.Err(); err != nil {
		return dnfrepo.AccountRecord{}, err
	}
	if o == nil || o.repositories.Account == nil || o.repositories.Character == nil {
		return dnfrepo.AccountRecord{}, ErrUnavailable
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || !validID(sourceID) {
		return dnfrepo.AccountRecord{}, ErrInvalidCharacter
	}
	account, found, err := o.repositories.Account.Load(ctx, accountID)
	if err != nil {
		return dnfrepo.AccountRecord{}, err
	}
	if !found || account.AccountID != accountID {
		return dnfrepo.AccountRecord{}, ErrAccountMissing
	}
	source, found, err := o.repositories.Character.Load(ctx, sourceID)
	if err != nil {
		return dnfrepo.AccountRecord{}, err
	}
	if !found || source.CharacterID != sourceID || source.AccountID != accountID {
		return dnfrepo.AccountRecord{}, ErrSourceNotOwned
	}
	return account, nil
}

// Current native character UID fields are u16. Reject truncation, zero and
// alternate textual encodings instead of creating aliases for repository IDs.
func validID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 16)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
func metadataKey(sourceID, targetID string) string { return metadataPrefix + sourceID + ":" + targetID }
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
