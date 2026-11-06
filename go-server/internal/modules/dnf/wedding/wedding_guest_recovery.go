package wedding

import (
	"context"
	"sort"
)

type GuestRunSnapshot struct {
	Key       uint32
	Reference GuestRunReference
}

// The run index is a durable source for startup recovery, including weddings
// whose participants are all offline. It contains no socket/session state.
func (o Owner) GuestRuns(ctx context.Context) ([]GuestRunSnapshot, error) {
	if o.Repositories.Settings == nil {
		return nil, ErrUnavailable
	}
	_, index, e := loadGuestIndex(ctx, o.Repositories.Settings)
	if e != nil {
		return nil, e
	}
	keys := make([]uint32, 0, len(index.Runs))
	for key := range index.Runs {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	rows := make([]GuestRunSnapshot, 0, len(keys))
	for _, key := range keys {
		if index.Runs[key].Closed {
			continue
		}
		rows = append(rows, GuestRunSnapshot{Key: key, Reference: index.Runs[key]})
	}
	return rows, nil
}
