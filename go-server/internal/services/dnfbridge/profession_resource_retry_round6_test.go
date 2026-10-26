package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// A request timeout is not a permanent error in the immutable character data.
// Warm only the skill catalogue, then interrupt profession profile loading and
// retry on the SAME service. Recreating Service would hide the poisoned cache.
func TestRealPVFProfessionResourcesRetryAfterInterruptedLoadRound6(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		want error
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"canceled", context.Canceled, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}},
		{"deadline", context.DeadlineExceeded, func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Unix(0, 0))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{initialEquipmentArchive: archive}
			skills, err := s.loadSkillCatalog(context.Background(), archive)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := test.ctx()
			defer cancel()
			profiles, _, err := s.currentProfessionResources(ctx)
			if !errors.Is(err, test.want) || profiles != nil {
				t.Fatalf("interrupted profiles=%v err=%v want %v", profiles, err, test.want)
			}
			if s.professionProfilesErr != nil {
				t.Fatalf("request interruption poisoned the service cache: %v", s.professionProfilesErr)
			}
			profiles, reloadedSkills, err := s.currentProfessionResources(context.Background())
			if err != nil || profiles == nil || profiles.Snapshot().Jobs == 0 || reloadedSkills != skills {
				t.Fatalf("fresh request cannot recover: profiles=%v skills_reused=%t err=%v", profiles, reloadedSkills == skills, err)
			}
			again, _, err := s.currentProfessionResources(context.Background())
			if err != nil || again != profiles {
				t.Fatal("successful profile cache was not reused", err)
			}
			// Resource corruption remains sticky; this fix must not turn it
			// into a retry loop or silently publish a partial profile.
			permanent := errors.New("invalid immutable profession resource")
			broken := &Service{initialEquipmentArchive: archive, skillCatalog: skills, professionProfilesErr: permanent}
			failed, _, err := broken.currentProfessionResources(context.Background())
			if failed != nil || !errors.Is(err, permanent) || broken.professionProfilesErr != permanent {
				t.Fatal("permanent resource failure was not preserved", err)
			}
		})
	}
}
