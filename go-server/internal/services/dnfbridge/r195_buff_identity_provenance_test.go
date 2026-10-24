package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The public candidate only preserves authentication provenance. It does not
// enable or claim strict login semantics on the public baseline.
func TestR195BuffIdentityCapturePreservesAccountProvenance(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authenticated bool
	}{
		{name: "verified", authenticated: true},
		{name: "unverified", authenticated: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			_, s, session, _ := reviewOwnedSendSeed(t)
			session.accountFromToken = tc.authenticated
			identity, ok := s.captureBuffOwnedSendIdentity(session)
			if !ok || identity.accountFromToken != tc.authenticated || identity.accountID == "" {
				t.Fatalf("captured identity=%+v ok=%t, want accountFromToken=%t", identity, ok, tc.authenticated)
			}
			s.mu.Lock()
			session.accountFromToken = !tc.authenticated
			s.mu.Unlock()
			if s.buffOwnedSendIdentityCurrent(identity) {
				t.Fatal("captured identity survived an account provenance change")
			}
		})
	}
}

// Account provenance and durable ownership remain separate checks. A verified
// snapshot cannot cross to another account, and revocation during a read
// invalidates the snapshot before send without asserting a public strict-login
// policy.
func TestR195BuffIdentityReapplyOwnershipAndChangeBoundaries(t *testing.T) {
	for _, name := range []string{"authenticated", "foreign_owner", "revoked_during_read"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			s, session, conn, r := r191CacheFixture(t)
			session.accountFromToken = true
			session.characterGeneration = 11
			s.gameSessions = map[uint16]*gameSession{1: session}
			if err := s.handleGameCommand(session, 1, 1426, reviewCurrentBuffCommand(38, 12, 0, 54)); err != nil {
				t.Fatal(err)
			}
			reviewCurrentBuffSnapshot(t, conn, 38, true)
			if name == "foreign_owner" {
				session.accountID = "dnf:other-owner"
			}
			before, _, err := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			if err != nil {
				t.Fatal(err)
			}
			if name == "revoked_during_read" {
				s.repositoryProvider = func() (repo.Group, bool) {
					s.mu.Lock()
					session.accountFromToken = false
					s.mu.Unlock()
					return r, true
				}
			}
			if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "authenticated_wear"); err != nil {
				t.Fatal(err)
			}
			if name == "authenticated" {
				r195ExpectBuffRows(t, conn, true, 54)
			} else if conn.write.Len() != 0 {
				t.Fatalf("foreign or revoked identity emitted %d bytes", conn.write.Len())
			}
			after, _, err := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("identity projection changed durable registration")
			}
		})
	}
}
