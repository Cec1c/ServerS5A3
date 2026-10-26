package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// PVPTotalUnitOfWork is implemented by the account asset owner. A frozen
// three-character roster and its account progress commit in one transaction.
type PVPTotalUnitOfWork interface {
	WithinPVPTotal(context.Context, string, [3]string, func(AccountRepository, CharacterRepository) error) error
}

func PVPTotalTransactionIDs(accountID string, ids [3]string) ([]string, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, fmt.Errorf("PVP total account missing")
	}
	result := append([]string(nil), ids[:]...)
	sort.Strings(result)
	for i, id := range result {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || i > 0 && id == result[i-1] {
			return nil, fmt.Errorf("PVP total requires three distinct character IDs")
		}
	}
	return result, nil
}
