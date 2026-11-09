package dnfbridge

import (
	"context"
	"crypto/sha256"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func utilityFixture(t *testing.T, slots map[string]repo.ItemStack) (*Service, *gameSession, repo.Group, boundGameSessionCharacter) {
	t.Helper()
	t.Setenv("DNFBRIDGE_PLAYER_HELPER_REFERENCE", "on")
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	a, e := items.ResolveItem(700)
	if e != nil {
		t.Fatal(e)
	}
	a.EquipmentType = "[weapon]"
	a.Rarity = 0
	items.itemCache[700] = a
	b, e := items.ResolveItem(701)
	if e != nil {
		t.Fatal(e)
	}
	b.EquipmentType = "[coat]"
	b.Rarity = 4
	items.itemCache[701] = b
	g := mustCurrentNPCShopRepositories(t, 10000, slots)
	s := currentNPCShopTestService(g, shop, items)
	p := &gameSession{conn: &bufferConn{}, connID: "utility-test", accountID: "account-1", selectedCharacterID: 19, accountFromToken: true, loginPacketSeen: true}
	p.townSceneReadyCharacterID = 19
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, PositionValid: true}
	p.setFinishLoadingGates(finishLoadingGates{})
	p.helperActionsAccessDigest = sha256.Sum256([]byte(helperActionCredential("test")))
	p.helperAccessDigest = sha256.Sum256([]byte(helperCredential("test")))
	s.bindGameSessionCharacter(p, 19)
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	return s, p, g, id
}
func utilityPreview(t *testing.T, s *Service, id boundGameSessionCharacter, kind string, mask byte, slot int16) helperUtilityReply {
	t.Helper()
	v, e := s.applyHelperUtility(context.Background(), id, helperUtilityRequest{Mode: "preview", Kind: kind, Binding: helperBinding(id), RequestID: launcherNonce(), RarityMask: mask, Slot: slot})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func utilityCommit(id boundGameSessionCharacter, v helperUtilityReply) helperUtilityRequest {
	return helperUtilityRequest{Mode: "commit", Binding: helperBinding(id), RequestID: launcherNonce(), Token: v.Token}
}
func TestPlayerUtilityAuthDomainAndStrictTarget(t *testing.T) {
	s, p, _, _ := utilityFixture(t, nil)
	api := accountAPIServer{service: s}
	req := func(secret, body string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/account/helper", strings.NewReader(body))
		r.Header.Set("X-DNF-Helper", secret)
		api.handlePlayerHelper(w, r)
		return w.Code
	}
	body := `{"version":2,"action":"utility","operation":{"mode":"status"}}`
	if req(helperActionCredential("test"), body) != 200 {
		t.Fatal("action capability refused")
	}
	for _, secret := range []string{helperCredential("test"), "test", helperActionCredential("other"), ""} {
		if req(secret, body) != 401 {
			t.Fatal("wrong capability accepted")
		}
	}
	if req(helperActionCredential("test"), `{"version":2,"action":"utility","operation":{"mode":"status","characterId":20}}`) != 400 {
		t.Fatal("target injection")
	}
	p.loginRevoked.Store(true)
	if req(helperActionCredential("test"), body) != 401 {
		t.Fatal("revoked capability")
	}
}
func TestPlayerUtilitySaleFilterPreviewReplayAndStale(t *testing.T) {
	slots := map[string]repo.ItemStack{"0:9": {ItemID: 700, Count: 1}, "0:10": {ItemID: 701, Count: 1}, "0:11": {ItemID: 700, Count: 1, Extra: map[string]string{"upgrade_level": "12"}}, "0:12": {ItemID: 700, Count: 1, Extra: map[string]string{repo.SortLockExtraKey: "1"}}, "0:4": {ItemID: 700, Count: 1}, "0:65": {ItemID: 600, Count: 3}}
	s, p, g, id := utilityFixture(t, slots)
	ctx := context.Background()
	preview := utilityPreview(t, s, id, "sell", 3, 0)
	if preview.Count != 1 || preview.Items[0].Slot != 9 || preview.Gold != 100 {
		t.Fatal(preview)
	}
	before, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(before.Slots, slots) {
		t.Fatal("preview wrote")
	}
	q := utilityCommit(id, preview)
	out, e := s.applyHelperUtility(ctx, id, q)
	if e != nil || !out.Committed {
		t.Fatal(out, e)
	}
	c, _, _ := g.Character.Load(ctx, "19")
	if c.Stats["gold"] != 10100 {
		t.Fatal(c.Stats)
	}
	after, _, _ := g.Inventory.Load(ctx, "19")
	if _, ok := after.Slots["0:9"]; ok {
		t.Fatal("not sold")
	}
	if len(after.Slots) != 5 {
		t.Fatal("protected consumed", after.Slots)
	}
	if again, e := s.applyHelperUtility(ctx, id, q); e != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("replay", again, e)
	}
	c, _, _ = g.Character.Load(ctx, "19")
	if c.Stats["gold"] != 10100 {
		t.Fatal("duplicate payout")
	}
	preview = utilityPreview(t, s, id, "sell", 16, 0)
	after.Slots["0:65"] = repo.ItemStack{ItemID: 600, Count: 4}
	_ = g.Inventory.Save(ctx, after)
	if _, e := s.applyHelperUtility(ctx, id, utilityCommit(id, preview)); !errors.Is(e, errHelperAssetsChanged) {
		t.Fatal("changed bag", e)
	}
	after2, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(after.Slots, after2.Slots) {
		t.Fatal("stale mutated")
	}
	p.helperUtility.Plan.Expires = time.Now().Add(-time.Second)
	if _, e := s.applyHelperUtility(ctx, id, utilityCommit(id, preview)); e == nil {
		t.Fatal("expired accepted")
	}
}
func TestPlayerUtilitySaleAtomicRollback(t *testing.T) {
	s, _, g, id := utilityFixture(t, map[string]repo.ItemStack{"0:9": {ItemID: 700, Count: 1}, "0:10": {ItemID: 700, Count: 1}})
	v := utilityPreview(t, s, id, "sell", 1, 0)
	id.session.helperUtility.Plan.Slots = append(id.session.helperUtility.Plan.Slots, 999)
	if _, e := s.applyHelperUtility(context.Background(), id, utilityCommit(id, v)); e == nil {
		t.Fatal("bad batch accepted")
	}
	c, _, _ := g.Character.Load(context.Background(), "19")
	i, _, _ := g.Inventory.Load(context.Background(), "19")
	if c.Stats["gold"] != 10000 || len(i.Slots) != 2 {
		t.Fatal("partial committed", c.Stats, i.Slots)
	}
}
func TestPlayerUtilityDisjointUsesExistingTransaction(t *testing.T) {
	s, _, g, id := utilityFixture(t, map[string]repo.ItemStack{"0:9": {ItemID: 5001, Count: 1}, "0:10": {ItemID: 5000, Count: 1, Extra: map[string]string{"upgrade_level": "1"}}})
	s.pvfItemCatalog = aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	seedCurrentDisjointAccount(t, g, nil)
	v := utilityPreview(t, s, id, "disjoint", 64, 0)
	if v.Count != 1 {
		t.Fatal(v)
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "19")
	inv.Slots["0:65"] = repo.ItemStack{ItemID: 3037, Count: 1}
	_ = g.Inventory.Save(context.Background(), inv)
	if _, e := s.applyHelperUtility(context.Background(), id, utilityCommit(id, v)); !errors.Is(e, errHelperAssetsChanged) {
		t.Fatal("stale disjoint", e)
	}
	v = utilityPreview(t, s, id, "disjoint", 64, 0)
	q := utilityCommit(id, v)
	if _, e := s.applyHelperUtility(context.Background(), id, q); e != nil {
		t.Fatal(e)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "19")
	if _, ok := after.Slots["0:9"]; ok {
		t.Fatal("not disjointed")
	}
	if after.Slots["0:10"].ItemID != 5000 {
		t.Fatal("enhanced consumed")
	}
	if _, e := s.applyHelperUtility(context.Background(), id, q); e != nil {
		t.Fatal(e)
	}
	again, _, _ := g.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(after, again) {
		t.Fatal("duplicate reward")
	}
}
func TestPlayerUtilityWarehouseRoundTripAndTownGuard(t *testing.T) {
	s, p, g, id := utilityFixture(t, map[string]repo.ItemStack{"0:65": {ItemID: 600, Count: 7}})
	ctx := context.Background()
	v := utilityPreview(t, s, id, "deposit", 0, 65)
	if _, e := s.applyHelperUtility(ctx, id, utilityCommit(id, v)); e != nil {
		t.Fatal(e)
	}
	inv, _, _ := g.Inventory.Load(ctx, "19")
	if inv.Warehouse["2:0"].Count != 7 {
		t.Fatal(inv)
	}
	v = utilityPreview(t, s, id, "withdraw", 0, 0)
	if _, e := s.applyHelperUtility(ctx, id, utilityCommit(id, v)); e != nil {
		t.Fatal(e)
	}
	inv, _, _ = g.Inventory.Load(ctx, "19")
	var n int64
	for _, a := range inv.Slots {
		if a.ItemID == 600 {
			n += a.Count
		}
	}
	if n != 7 || len(inv.Warehouse) != 0 {
		t.Fatal("loss/duplicate", inv)
	}
	_, r := prepareSyntheticMoveRuntime(t, false)
	p.dungeon.runtime = r
	if e := s.helperUtilityGuard(p, "warehouse"); e == nil {
		t.Fatal("dungeon warehouse")
	}
}
func TestPlayerUtilityRoomGuardsAndWholeParty(t *testing.T) {
	s, p, f := partyMovePreflightFixture(t)
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	q := helperUtilityRequest{Mode: "room", Binding: helperBinding(id), RequestID: launcherNonce(), Room: s.helperRoomStamp(p), Direction: "right"}
	fid, _ := s.boundGameSessionCharacterSnapshot(f)
	fq := q
	fq.Binding = helperBinding(fid)
	fq.Room = s.helperRoomStamp(f)
	if _, e := s.applyHelperUtility(context.Background(), fid, fq); e == nil {
		t.Fatal("follower moved")
	}
	if _, e := s.applyHelperUtility(context.Background(), id, q); e != nil {
		t.Fatal(e)
	}
	for _, member := range []*gameSession{p, f} {
		scene, _ := member.dungeon.runtime.Session.Scene()
		if scene.Coordinate.X != 1 {
			t.Fatal("party split", scene)
		}
	}
	before := p.conn.(*bufferConn).write.Len()
	if _, e := s.applyHelperUtility(context.Background(), id, q); e != nil {
		t.Fatal(e)
	}
	if before != p.conn.(*bufferConn).write.Len() {
		t.Fatal("replayed move")
	}
	s2, r := prepareSyntheticMoveRuntime(t, true)
	p2 := &gameSession{conn: &bufferConn{}, connID: "blocked", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
	s2.bindGameSessionCharacter(p2, 99)
	id2, _ := s2.boundGameSessionCharacterSnapshot(p2)
	q.Binding = helperBinding(id2)
	q.RequestID = launcherNonce()
	q.Room = s2.helperRoomStamp(p2)
	if _, e := s2.applyHelperUtility(context.Background(), id2, q); e == nil {
		t.Fatal("uncleared moved")
	}
}

func TestPlayerUtilityTownReadyDoesNotRequireOptionalDungeonPostFinish(t *testing.T) {
	s, p, _, id := utilityFixture(t, map[string]repo.ItemStack{"0:9": {ItemID: 700, Count: 1}})
	if p.finishLoadingGateSnapshot().PostFinish {
		t.Fatal("fixture must match the real town path")
	}
	for _, kind := range []string{"status", "warehouse", "sell", "disjoint", "repair"} {
		if e := s.helperUtilityGuard(p, kind); e != nil {
			t.Fatalf("ready town %s refused: %v", kind, e)
		}
	}
	status, e := s.applyHelperUtility(context.Background(), id, helperUtilityRequest{Mode: "status"})
	if e != nil || status.Message != "便捷功能已就绪" {
		t.Fatal(status, e)
	}
	p.townSceneReadyCharacterID = 0
	for _, kind := range []string{"warehouse", "sell", "disjoint", "repair"} {
		if e := s.helperUtilityGuard(p, kind); e == nil {
			t.Fatalf("unready town %s accepted", kind)
		}
	}
	p.townSceneReadyCharacterID = 19
	p.townPositionSnapshot.PositionValid = false
	if e := s.helperUtilityGuard(p, "sell"); e == nil {
		t.Fatal("missing position accepted")
	}
	p.townPositionSnapshot.PositionValid = true
	p.channelExitAcknowledged = true
	if e := s.helperUtilityGuard(p, "sell"); e == nil {
		t.Fatal("exited channel accepted")
	}
}

func TestPlayerUtilityNativeWarehouseAllowsLoadedDungeonWithoutAssetChanges(t *testing.T) {
	ctx := context.Background()
	s, p, g, id := utilityFixture(t, map[string]repo.ItemStack{"0:9": {ItemID: 600, Count: 7}})
	before, _, err := g.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	request := func() (helperUtilityReply, error) {
		return s.applyHelperUtility(ctx, id, helperUtilityRequest{Mode: "warehouse-open", Binding: helperBinding(id), RequestID: launcherNonce()})
	}
	assertReady := func() {
		t.Helper()
		out, err := request()
		if err != nil || !out.OK || out.Kind != "native-warehouse" || len(out.Items) != 0 || out.Committed || out.Token != "" {
			t.Fatal(out, err)
		}
	}
	assertReady()
	_, runtime := prepareSyntheticMoveRuntime(t, false)
	runtime.Character.CharacterID = "19"
	p.dungeon.runtime = runtime
	if _, err := request(); err == nil {
		t.Fatal("loading accepted")
	}
	p.setFinishLoadingGates(finishLoadingGates{State: true, Completion: true, PostFinish: true})
	assertReady() // No clear-room requirement: warehouse is available during the dungeon.
	runtime.townReturnPending = true
	if _, err := request(); err == nil {
		t.Fatal("return transition accepted")
	}
	runtime.townReturnPending = false
	runtime.deathReturnWaiting = true
	if _, err := request(); err == nil {
		t.Fatal("death return accepted")
	}
	runtime.deathReturnWaiting = false
	runtime.Character.CharacterID = "20"
	if _, err := request(); err == nil {
		t.Fatal("another dungeon owner accepted")
	}
	runtime.Character.CharacterID = "19"
	if _, err := s.applyHelperUtility(ctx, id, helperUtilityRequest{Mode: "warehouse-open", Binding: "wrong", RequestID: launcherNonce()}); err == nil {
		t.Fatal("stale binding accepted")
	}
	p.loginRevoked.Store(true)
	if _, err := request(); err == nil {
		t.Fatal("revoked login accepted")
	}
	p.loginRevoked.Store(false)
	after, _, err := g.Inventory.Load(ctx, "19")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("opening mutated inventory", before, after, err)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("UI permission emitted a forged game result")
	}
}
