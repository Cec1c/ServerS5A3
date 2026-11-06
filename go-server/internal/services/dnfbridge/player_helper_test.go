package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func helperFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, repo.Group, *bufferConn) {
	t.Helper()
	t.Setenv("DNFBRIDGE_PLAYER_HELPER_REFERENCE", "on")
	s, r, g := prepareCurrentDungeonDropTest(t, 1, 3227)
	if err := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"preserve": "same", "helper_auto_pickup_v1:99": "on"}}); err != nil {
		t.Fatal(err)
	}
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	conn := &bufferConn{}
	p := &gameSession{conn: conn, connID: "helper-fixture", accountID: "account-1", accountFromToken: true, loginPacketSeen: true, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
	p.helperAccessDigest = sha256.Sum256([]byte(helperCredential("test login token")))
	s.bindGameSessionCharacter(p, 99)
	return s, p, r, g, conn
}
func helperApply(t *testing.T, s *Service, p *gameSession, policy helperLootPolicy) helperLootReply {
	t.Helper()
	id, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		t.Fatal("not bound")
	}
	out, err := s.applyHelperLoot(p, id, helperLootRequest{Action: "apply", Binding: helperBinding(id), Owner: launcherNonce(), Revision: p.helper.revision, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestPlayerHelperFilterEveryRarityAndType(t *testing.T) {
	for n := int64(0); n < 7; n++ {
		for m := int64(0); m < 7; m++ {
			for _, kind := range []dungeonDropItemKind{dungeonDropItemEquipment, dungeonDropItemStackable} {
				p := helperLootPolicy{EquipmentMask: 1 << uint(n), StackableMask: 1 << uint(n)}
				d := &runtimeDungeonDrop{Item: dungeonDropItemDefinition{ItemID: 1, Kind: kind, Rarity: m}}
				if p.accepts(d) != (n == m) {
					t.Fatal(n, m, kind)
				}
			}
		}
	}
	p := helperLootPolicy{EquipmentMask: 127}
	if p.accepts(&runtimeDungeonDrop{}) || p.accepts(&runtimeDungeonDrop{Item: dungeonDropItemDefinition{ItemID: 1, Kind: dungeonDropItemStackable, Rarity: 0}}) {
		t.Fatal("type/gold bypass")
	}
	if !(helperLootPolicy{Gold: true}).accepts(&runtimeDungeonDrop{}) {
		t.Fatal("gold")
	}
	for _, r := range []int64{-1, 7, 255} {
		if p.accepts(&runtimeDungeonDrop{Item: dungeonDropItemDefinition{ItemID: 1, Kind: dungeonDropItemEquipment, Rarity: r}}) {
			t.Fatal("unknown rarity")
		}
	}
	if (helperLootPolicy{}).valid() || (helperLootPolicy{EquipmentMask: 128}).valid() {
		t.Fatal("invalid accepted")
	}
}
func TestPlayerHelperFilterBeforeLimitAndOwnership(t *testing.T) {
	s, _, r, _, _ := helperFixture(t)
	actor := currentSceneActorObjectKey(99)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, actor)
	base := *r.DropOwner.byObjectKey[key]
	for i := uint32(0); i < 15; i++ {
		d := base
		d.ObjectKey = key + i
		d.Item.Kind = dungeonDropItemEquipment
		d.Item.Rarity = 0
		if i >= 8 {
			d.Item.Rarity = 4
		}
		r.DropOwner.byObjectKey[d.ObjectKey] = &d
	}
	r.DropOwner.byObjectKey[key+8].PartyShared = true
	r.DropOwner.byObjectKey[key+9].OwnerActorObjectKey = 1
	r.DropOwner.byObjectKey[key+10].DiscardOrigin = &runtimeDungeonDiscardOrigin{}
	r.DropOwner.byObjectKey[key+11].Room.MapID++
	got := currentHelperPickupKeys(r, actor, helperLootPolicy{EquipmentMask: 1 << 4})
	if len(got) != 3 || got[0] != key+12 {
		t.Fatal(got)
	}
}
func TestPlayerHelperTransactionsLeaseExpiryAndGeneration(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, g, conn := helperFixture(t)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	policy := helperLootPolicy{StackableMask: 127}
	lease := helperApply(t, s, p, policy)
	if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
		t.Fatal(err)
	}
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropConsumed {
		t.Fatal("not consumed")
	}
	before := conn.write.Len()
	if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
		t.Fatal(err)
	}
	if before != conn.write.Len() {
		t.Fatal("replayed grant")
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "99")
	if len(inv.Slots) != 1 {
		t.Fatal(inv)
	}
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	q := helperLootRequest{Action: "renew", Binding: lease.Binding, Owner: p.helper.owner, Lease: lease.Lease, Sequence: 1}
	if _, err := s.applyHelperLoot(p, id, q); err != nil {
		t.Fatal(err)
	}
	p.helper.leaseUntil = s.gameplayNow().Add(-time.Second)
	if _, err := s.applyHelperLoot(p, id, q); err == nil {
		t.Fatal("expired renewed")
	}
	if p.helper.enabled {
		t.Fatal("expired enabled")
	}
	helperApply(t, s, p, policy)
	serial := p.helper.generation
	s.stopCurrentPlayerHelper(p)
	before = conn.write.Len()
	_ = s.runCurrentPlayerHelper(p, serial)
	if before != conn.write.Len() {
		t.Fatal("old generation acted")
	}
}
func TestPlayerHelperReleaseReapplyAndReplayAreScoped(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, _, _, _ := helperFixture(t)
	a := helperApply(t, s, p, helperLootPolicy{Gold: true})
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	owner := p.helper.owner
	release := helperLootRequest{Action: "release", Binding: a.Binding, Owner: owner, Lease: a.Lease}
	if _, err := s.applyHelperLoot(p, id, release); err != nil {
		t.Fatal(err)
	}
	b := helperApply(t, s, p, helperLootPolicy{EquipmentMask: 16})
	if _, err := s.applyHelperLoot(p, id, release); err == nil {
		t.Fatal("old release accepted")
	}
	if p.helper.lease != b.Lease || !p.helper.enabled {
		t.Fatal("new lease stopped")
	}
	q := helperLootRequest{Action: "renew", Binding: b.Binding, Owner: p.helper.owner, Lease: b.Lease, Sequence: 2}
	if _, err := s.applyHelperLoot(p, id, q); err != nil {
		t.Fatal(err)
	}
	expiry := p.helper.leaseUntil
	if _, err := s.applyHelperLoot(p, id, q); err != nil || p.helper.leaseUntil != expiry {
		t.Fatal("replayed renew extended")
	}
	q.Sequence = 1
	if _, err := s.applyHelperLoot(p, id, q); err == nil {
		t.Fatal("old sequence accepted")
	}
	q.Binding = "wrong"
	if _, err := s.applyHelperLoot(p, id, q); err == nil {
		t.Fatal("wrong binding")
	}
}
func TestPlayerHelperLegacyOnCannotUnfilteredActivateAndOffRevokesLease(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, _, g, _ := helperFixture(t)
	if err := s.startCurrentPlayerHelper(p); err != nil {
		t.Fatal(err)
	}
	if p.helper.enabled {
		t.Fatal("old metadata auto enabled")
	}
	_, err := s.handleCurrentHelperChat(context.Background(), p, 17, []byte("!loot on"))
	if err != nil || p.helper.enabled {
		t.Fatal(err)
	}
	helperApply(t, s, p, helperLootPolicy{Gold: true})
	_, err = s.handleCurrentHelperChat(context.Background(), p, 17, []byte("!loot off"))
	if err != nil || p.helper.enabled {
		t.Fatal(err)
	}
	helperApply(t, s, p, helperLootPolicy{Gold: true})
	if scheduled, _, active := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue).counts(); scheduled != 2 || active != 1 {
		t.Fatal("chat stop prevented next activation", scheduled, active)
	}
	a, _, _ := g.Account.Load(context.Background(), "account-1")
	if a.Metadata["preserve"] != "same" {
		t.Fatal("metadata changed")
	}
}
func TestPlayerHelperHTTPIsNotPIDOrAccountSelectable(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, _, _, _ := helperFixture(t)
	api := &accountAPIServer{service: s}
	request := func(secret, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/account/helper", strings.NewReader(body))
		r.Header.Set("X-DNF-Helper", secret)
		api.handlePlayerHelper(w, r)
		return w
	}
	body := `{"version":1,"action":"status"}`
	if w := request(helperCredential("test login token"), body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, secret := range []string{"", strings.Repeat("0", 64), helperCredential("other token"), "test login token"} {
		if request(secret, body).Code != 401 {
			t.Fatal("bad auth")
		}
	}
	if request(helperCredential("test login token"), `{"version":1,"action":"status","characterId":"100"}`).Code != 400 {
		t.Fatal("injected target accepted")
	}
	p.loginRevoked.Store(true)
	if request(helperCredential("test login token"), body).Code != 401 {
		t.Fatal("revoked accepted")
	}
}
func TestPlayerHelperFailurePausesWithoutConsumingAndGoldIsIndependent(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, g, _ := helperFixture(t)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	failing := g
	failing.CharacterItems = pickupTestItemUnitOfWork{inventory: pickupFailingInventoryRepository{InventoryRepository: g.Inventory, err: errDungeonPickupInventoryFull}, equipment: g.Equipment}
	s.repositoryProvider = func() (repo.Group, bool) { return failing, true }
	if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
		t.Fatal(err)
	}
	if !p.helper.paused || r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
		t.Fatal("failed grant consumed")
	}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	r.DropOwner.byObjectKey[key].Item = dungeonDropItemDefinition{}
	r.DropOwner.byObjectKey[key].Amount = 123
	helperApply(t, s, p, helperLootPolicy{Gold: true})
	if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
		t.Fatal(err)
	}
	c, _, _ := g.Character.Load(context.Background(), "99")
	if c.Stats["gold"] != 123 {
		t.Fatal(c.Stats)
	}
}
func TestPlayerHelperJSONNeverIncludesCapability(t *testing.T) {
	b, _ := json.Marshal(helperLootReply{OK: true})
	if strings.Contains(string(b), "token") {
		t.Fatal(string(b))
	}
}

func TestPlayerHelperDeadOrDepartedRoomNeverConsumes(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, _, _ := helperFixture(t)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	r.deathReturnWaiting = true
	if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
		t.Fatal(err)
	}
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
		t.Fatal("dead player picked up")
	}
	r.deathReturnWaiting = false
	r.DropOwner.byObjectKey[key].Room.MapID++
	if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
		t.Fatal(err)
	}
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
		t.Fatal("stale room picked up")
	}
}

func TestPlayerHelperRecoveryHintOnlyForExpiredLease(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	for _, path := range []string{"api", "drop-worker"} {
		t.Run(path, func(t *testing.T) {
			s, p, _, _, _ := helperFixture(t)
			id, _ := s.boundGameSessionCharacterSnapshot(p)
			status := func() helperLootReply {
				r, err := s.applyHelperLoot(p, id, helperLootRequest{Action: "status"})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			if status().Recoverable {
				t.Fatal("fresh character is not an expired lease")
			}
			a := helperApply(t, s, p, helperLootPolicy{Gold: true})
			p.helper.leaseUntil = s.gameplayNow().Add(-time.Second)
			if path == "drop-worker" {
				if err := s.runCurrentPlayerHelper(p, p.helper.generation); err != nil {
					t.Fatal(err)
				}
			}
			out := status()
			if out.Enabled || !out.Recoverable || out.Revision != a.Revision+1 || p.helper.owner != "" || p.helper.lease != "" || p.helper.pending {
				t.Fatalf("bad expiry: %+v", out)
			}
			if status().Revision != out.Revision {
				t.Fatal("expiry replay changed revision")
			}
			if _, err := s.handleCurrentHelperChat(context.Background(), p, 17, []byte("!loot off")); err != nil {
				t.Fatal(err)
			}
			if status().Recoverable {
				t.Fatal("manual off after expiry must stay off")
			}
			a = helperApply(t, s, p, helperLootPolicy{Gold: true})
			if status().Recoverable {
				t.Fatal("new lease retained expiry hint")
			}
			if _, err := s.applyHelperLoot(p, id, helperLootRequest{Action: "release", Binding: a.Binding, Owner: p.helper.owner, Lease: a.Lease}); err != nil {
				t.Fatal(err)
			}
			if status().Recoverable {
				t.Fatal("explicit release must stay off")
			}
			helperApply(t, s, p, helperLootPolicy{Gold: true})
			p.helper.leaseUntil = s.gameplayNow().Add(-time.Second)
			if !status().Recoverable {
				t.Fatal("missing second expiry")
			}
			s.stopCurrentPlayerHelper(p)
			if status().Recoverable {
				t.Fatal("character stop retained expiry hint")
			}
			encoded, err := json.Marshal(status())
			if err != nil || strings.Contains(string(encoded), "recoverable") {
				t.Fatal("false hint must be absent for older clients", string(encoded), err)
			}
		})
	}
}

func TestPlayerHelperStatusConfirmsOnlyMatchingActiveLease(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, _, _, _ := helperFixture(t)
	a := helperApply(t, s, p, helperLootPolicy{Gold: true})
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	q := helperLootRequest{Action: "status", Binding: a.Binding, Owner: p.helper.owner, Lease: a.Lease}
	out, err := s.applyHelperLoot(p, id, q)
	if err != nil || !out.Enabled || !out.CurrentLease || out.Recoverable || out.Lease != "" {
		t.Fatal(out, err)
	}
	revision, until := p.helper.revision, p.helper.leaseUntil
	for _, bad := range []helperLootRequest{{Action: "status"}, {Action: "status", Owner: q.Owner, Lease: launcherNonce()}, {Action: "status", Owner: launcherNonce(), Lease: q.Lease}} {
		out, err = s.applyHelperLoot(p, id, bad)
		if err != nil || out.CurrentLease || !out.Enabled || p.helper.revision != revision || p.helper.leaseUntil != until {
			t.Fatal(out, err)
		}
	}
}
