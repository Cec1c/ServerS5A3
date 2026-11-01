package dnfbridge

import (
	"context"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestPrivateSelectionReloadsAfterGMFence(t *testing.T) {
	for _, change := range []string{"stats", "delete", "owner", "slot"} {
		t.Run(change, func(t *testing.T) {
			fixture := newSelectionReadsFixture(1)
			repos, characters, _, _ := selectionReadsTestGroup(fixture)
			repos.Equipment = nil
			called := false
			repos.Account = admissionFenceFixture{AccountRepository: repos.Account, wait: func(context.Context, string) error {
				called = true
				if len(characters.loadIDs) != 0 {
					t.Fatal("full record read before GM fence")
				}
				ch := fixture.records["700"]
				switch change {
				case "stats":
					ch.Name = "after-gm"
					ch.Stats["selection_probe_marker"] = 42
				case "delete":
					ch.Stats["delete_flag"] = 1
				case "owner":
					ch.AccountID = "other"
				case "slot":
					ch.Slot++
				}
				fixture.records["700"] = ch
				return nil
			}}
			s := &Service{options: options{playerStrict: true, accountPrefix: defaultAccountPrefix}, repositoryProvider: func() (r.Group, bool) { return repos, true }}
			p := &gameSession{accountID: fixture.account, accountFromToken: true, currentRenameRoster: []r.CharacterRecord{fixture.rosterIdentity}}
			_, _, ch, found, _ := s.resolveSelectedCharacter(context.Background(), p, []byte{0, 0})
			if !called || len(characters.loadIDs) != 1 {
				t.Fatalf("fence=%v loads=%v", called, characters.loadIDs)
			}
			if change == "stats" {
				if !found || ch.Name != "after-gm" || ch.Stats["selection_probe_marker"] != 42 {
					t.Fatalf("stale snapshot: %+v", ch)
				}
			} else if found {
				t.Fatal("GM-changed identity accepted")
			}
		})
	}
}
