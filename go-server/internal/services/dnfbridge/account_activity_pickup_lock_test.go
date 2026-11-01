package dnfbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the packet owner, which already holds the shared drop lock, rather
// than calling the activity builder with an unlocked synthetic runtime.
func TestActivityEquipmentPickupCompletesAndReleasesLogin(t *testing.T) {
	for _, party := range []bool{true, false} {
		for _, rarity := range []int64{3, 4, 5} {
			t.Run(fmt.Sprintf("party_%t_rarity_%d", party, rarity), func(t *testing.T) {
				s, r, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
				key := seedCurrentDungeonPickupDrop(t, s, r, 9001, currentSceneActorObjectKey(99))
				drop := r.DropOwner.byObjectKey[key]
				drop.Item.Rarity = rarity
				if party {
					r.partyInstance = &dungeonPartyInstance{dropOwner: r.DropOwner, activityNonce: strings.Repeat("ab", 16)}
				}
				p := &gameSession{conn: &bufferConn{}, connID: "activity-pickup", accountID: "account-1", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
				if err := s.startGameSessionEvents(context.Background(), p); err != nil {
					t.Fatal(err)
				}
				defer stopTestGameSessionEvents(t, s, p)
				s.accountLogins.sessions = map[*gameSession]string{p: p.accountID}
				for attempt := 0; attempt < 2; attempt++ {
					if err := s.callGameSession(context.Background(), p, "game-stream-packet", func() error {
						return s.handleCurrentDungeonPickup(p, currentDungeonPickupTestBody(key))
					}); err != nil {
						t.Fatal(err)
					}
				}
				if drop.Status != runtimeDungeonDropConsumed || len(drop.PickupResponseBody) != currentDungeonPickupResultSize {
					t.Fatalf("pickup did not commit and acknowledge: %+v", drop)
				}
				inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
				if err != nil || !found || len(inventory.Slots) != 1 {
					t.Fatalf("pickup/replay inventory = %+v found=%t err=%v", inventory, found, err)
				}
				events, err := repositories.Activity.Events(context.Background(), repo.ActivityQuery{AccountID: p.accountID})
				if err != nil || len(events) != 1 || events[0].ItemID != 9001 || events[0].Rarity != rarity {
					t.Fatalf("pickup/replay activity = %+v err=%v", events, err)
				}
				if party && !strings.HasPrefix(events[0].ID, r.partyInstance.activityNonce+":") {
					t.Fatalf("pickup lost shared run identity: %s", events[0].ID)
				}
				s.accountLogins.mu.Lock()
				s.accountLogins.replace(p.accountID)
				done := s.accountLogins.retiring[p.accountID].done
				s.accountLogins.mu.Unlock()
				s.shutdownGameSessionEvents(p, false)
				waitTestSignal(t, done, "completed pickup left login retirement fenced")
			})
		}
	}
}
