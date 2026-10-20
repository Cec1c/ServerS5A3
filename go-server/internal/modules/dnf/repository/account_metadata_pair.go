package repository

import (
	"context"
	"strings"
	"time"
)

// AccountMetadataPairUpdater locks both existing owners, gives the domain
// authoritative snapshots, and commits only the returned keys atomically.
// It never saves a caller's stale whole-account metadata map.
type AccountMetadataPairUpdater interface {
	UpdateMetadataPair(context.Context, string, string, func(AccountRecord, AccountRecord) (map[string]string, map[string]string, error), time.Time) error
}

func ValidAccountMetadataChanges(changes map[string]string) bool {
	for key := range changes {
		if strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) {
			return false
		}
	}
	return true
}
