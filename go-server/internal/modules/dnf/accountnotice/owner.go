// Package accountnotice persists the current client's second notice group.
package accountnotice

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
	"time"
)

const Limit = 128
const prefix = "notice_2nd_viewed_"

var ErrInvalid = errors.New("invalid account notice identity or id")
var ErrUnavailable = errors.New("account notice repository unavailable")

func Load(ctx context.Context, accounts repo.AccountRepository, accountID string) ([]byte, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, ErrInvalid
	}
	if accounts == nil {
		return nil, ErrUnavailable
	}
	account, found, err := accounts.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrUnavailable
	}
	ids := make([]byte, 0)
	for id := 0; id < Limit; id++ {
		if account.Metadata[prefix+strconv.Itoa(id)] == "1" {
			ids = append(ids, byte(id))
		}
	}
	return ids, nil
}

func MarkViewed(ctx context.Context, accounts repo.AccountRepository, accountID string, id byte) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || int(id) >= Limit {
		return ErrInvalid
	}
	if accounts == nil {
		return ErrUnavailable
	}
	writer, ok := accounts.(repo.AccountMetadataEntryWriter)
	if !ok {
		return ErrUnavailable
	}
	_, found, err := accounts.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return ErrUnavailable
	}
	// One monotone key per notice avoids read/replace races with other notices
	// and unrelated account metadata. Both production stores support this write.
	return writer.SaveMetadataEntry(ctx, accountID, prefix+strconv.Itoa(int(id)), "1", time.Now().UTC())
}
