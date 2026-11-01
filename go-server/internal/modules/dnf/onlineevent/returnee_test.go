package onlineevent

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestReturneeStateUsesThirtyDayWindow(t *testing.T) {
	start := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	state := ReturneeState{Started: start, ExpiresAt: start.Add(ReturneeDuration)}
	if !state.ActiveAt(start.Add(29*24*time.Hour + 23*time.Hour)) {
		t.Fatal("returnee state expired before 30 days")
	}
	if state.ActiveAt(start.Add(ReturneeDuration)) {
		t.Fatal("returnee state remained active at the 30-day boundary")
	}

	account := dnfrepo.AccountRecord{Metadata: map[string]string{ReturneeMetadataKey: `{"started":"2026-09-15T08:00:00Z"}`}}
	loaded, err := LoadReturneeState(account)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ExpiresAt.Equal(start.Add(ReturneeDuration)) {
		t.Fatalf("legacy expiration=%v", loaded.ExpiresAt)
	}
}

func TestReturneeActivationStoresExpiration(t *testing.T) {
	now := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	state := ReturneeState{LastSeen: now.Add(-ReturneeOfflinePeriod)}
	state.Observe(&ReturneeCatalog{}, "character", ReturneeObservation{}, now, true)
	if !state.Started.Equal(now) || !state.ExpiresAt.Equal(now.Add(ReturneeDuration)) {
		t.Fatalf("activation window: started=%v expires=%v", state.Started, state.ExpiresAt)
	}
}
