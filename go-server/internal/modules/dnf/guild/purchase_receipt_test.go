package guild

import (
	"testing"
	"time"
)

func forEachPurchaseReceiptTimeZone(t *testing.T, run func(*testing.T, *time.Location)) {
	t.Helper()
	for _, zone := range []struct {
		name string
		loc  *time.Location
	}{
		{"UTC", time.UTC},
		// A zero-offset location serializes as Z and reloads as time.UTC.
		{"named-zero-offset", time.FixedZone("LocalUTC", 0)},
		{"UTC+8", time.FixedZone("UTC+8", 8*60*60)},
	} {
		t.Run(zone.name, func(t *testing.T) { run(t, zone.loc) })
	}
}

// JSON round trips preserve the instant, but may replace the location object.
// Keep exact comparison for every non-time field, including newly added fields.
func equalCargoPurchaseReceipt(got, want CargoPurchaseReceipt) bool {
	if !got.At.Equal(want.At) {
		return false
	}
	got.At = want.At
	return got == want
}

func equalBenefitPurchaseReceipt(got, want BenefitPurchaseReceipt) bool {
	if !got.At.Equal(want.At) || !got.Benefit.ExpiresAt.Equal(want.Benefit.ExpiresAt) {
		return false
	}
	got.At = want.At
	got.Benefit.ExpiresAt = want.Benefit.ExpiresAt
	return got == want
}
