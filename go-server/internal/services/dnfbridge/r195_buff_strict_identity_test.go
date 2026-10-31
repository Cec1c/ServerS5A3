package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Strict deployment must retain the authenticated provenance of the live
// account in the detached snapshot without accepting an unverified account.
func TestR195BuffStrictIdentityReapply(t *testing.T) {
	for _, name := range []string{"authenticated", "unverified", "foreign_owner", "revoked_during_read"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			s, session, conn, r := r191CacheFixture(t)
			s.options.playerStrict = true
			session.accountFromToken = true
			session.characterGeneration = 11
			s.gameSessions = map[uint16]*gameSession{1: session}
			if err := s.handleGameCommand(session, 1, 1426, reviewCurrentBuffCommand(38, 12, 0, 54)); err != nil {
				t.Fatal(err)
			}
			reviewCurrentBuffSnapshot(t, conn, 38, true)
			session.accountFromToken = name != "unverified"
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
			if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "strict_authenticated_wear"); err != nil {
				t.Fatal(err)
			}
			if name == "authenticated" {
				r195ExpectBuffRows(t, conn, true, 54)
			} else if conn.write.Len() != 0 {
				t.Fatalf("unverified, revoked or foreign identity emitted %d bytes", conn.write.Len())
			}
			after, _, err := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("identity projection changed durable registration")
			}
		})
	}
}

func TestR195BuffIdentityRejectsChangedTokenProvenance(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "strict"}[strict], func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			_, s, session, _ := reviewOwnedSendSeed(t)
			s.options.playerStrict = strict
			session.accountFromToken = true
			identity, ok := s.captureBuffOwnedSendIdentity(session)
			if !ok || identity.accountID == "" || !s.buffOwnedSendIdentityCurrent(identity) {
				t.Fatal("authenticated snapshot did not retain its account")
			}
			s.mu.Lock()
			session.accountFromToken = false
			s.mu.Unlock()
			if s.buffOwnedSendIdentityCurrent(identity) {
				t.Fatal("snapshot survived a token provenance change")
			}
		})
	}
}
