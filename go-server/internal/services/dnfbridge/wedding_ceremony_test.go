package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingCeremonyRecords() wedding.PairResult {
	result := wedding.PairResult{}
	result.Actor = r.CharacterRecord{CharacterID: "29", AccountID: "wedding:29", Job: "0", Name: "groom", Level: 90, Stats: map[string]int64{
		"wedding_v1_partner": 30, "wedding_v1_phase": int64(wedding.Reserved), "wedding_v1_ceremony_at": 4102444800,
		"wedding_v1_ceremony_channel": 10, "wedding_v1_ceremony_area": 8,
	}}
	result.Partner = r.CharacterRecord{CharacterID: "30", AccountID: "wedding:30", Job: "11", Name: "bride", Level: 90, Stats: map[string]int64{
		"wedding_v1_partner": 29, "wedding_v1_phase": int64(wedding.Reserved), "wedding_v1_ceremony_at": 4102444800,
		"wedding_v1_ceremony_channel": 10, "wedding_v1_ceremony_area": 8,
	}}
	return result
}

func weddingCeremonyLoginFixture(t *testing.T, ready bool) (*Service, boundGameSessionCharacter, r.Group) {
	t.Helper()
	s, first, second, _ := weddingRoomLeaseFixture(t)
	t.Cleanup(s.stopCurrentWeddingCeremonyTimers)
	table, err := town.Load(context.Background(), townMoveTestSource{
		"town/town.lst":   "39 `church.twn`",
		"town/church.twn": "[area]\n8 `Church/Church.map` `[normal]`\n[/area]",
	}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	now := time.Unix(1793534400, 0)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	s.wedding.rules.ForceMove = [2]wedding.CeremonyPosition{{X: 670, Y: 330}, {X: 685, Y: 360}}
	repos := memory.NewMemoryGroup()
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	pair := weddingCeremonyRecords()
	for _, c := range []*r.CharacterRecord{&pair.Actor, &pair.Partner} {
		c.Stats["wedding_v1_ceremony_at"] = now.Unix() - 30
		c.Stats["wedding_v1_ceremony_channel"] = int64(first.channel.ID)
		if ready {
			c.Stats["wedding_v1_ceremony_ready_at"] = now.Unix() - 30
		}
		c.Stats["town_id"], c.Stats["area_id"] = 38, 0
		if err := r.SaveCharacterFields(context.Background(), repos.Character, *c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Equipment.Save(context.Background(), r.EquipmentRecord{CharacterID: c.CharacterID, Entries: map[string]r.EquipmentEntry{}}); err != nil {
			t.Fatal(err)
		}
	}
	// The other spouse remains outside, so the timer cannot complete marriage.
	for _, p := range []*gameSession{first, second} {
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, TownID: 38, ChannelID: first.channel.ID, Session: p})
	}
	identity, _ := s.boundGameSessionCharacterSnapshot(first)
	return s, identity, repos
}

func assertWeddingCeremonyLoginRecovery(t *testing.T, s *Service, identity boundGameSessionCharacter, repos r.Group, townID int64, pending bool) {
	t.Helper()
	c, found, err := repos.Character.Load(context.Background(), strconv.Itoa(int(identity.character)))
	if err != nil || !found || c.Stats["town_id"] != townID {
		t.Fatalf("unexpected recovery location: town=%d found=%t err=%v", c.Stats["town_id"], found, err)
	}
	s.wedding.mu.Lock()
	login, recorded := s.wedding.ceremonyLogins[identity.character]
	s.wedding.mu.Unlock()
	if !recorded || login.owner != identity || login.pending != pending {
		t.Fatalf("unexpected login recovery ownership: recorded=%t owner=%+v pending=%t", recorded, login.owner, login.pending)
	}
}

func weddingCeremonyLoginSnapshot(t *testing.T, repos r.Group) *wedding.State {
	t.Helper()
	c, found, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load initial wedding state: %t %v", found, err)
	}
	state, err := wedding.Read(c)
	if err != nil {
		t.Fatal(err)
	}
	return &state
}

func TestWeddingCeremonyLoginRecoveryIsConsumedOncePerBinding(t *testing.T) {
	s, identity, repos := weddingCeremonyLoginFixture(t, true)
	if err := s.runCurrentWeddingCeremonyRecovery(identity, weddingCeremonyLoginSnapshot(t, repos)); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 39, false)
	// Leave the Church to make room in inventory. Neither a TownMove/finish
	// callback nor duplicate initial packets may rearm the consumed admission.
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Stats["town_id"], c.Stats["area_id"] = 38, 0
	if err := r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	for _, initial := range []bool{false, true, false, true} {
		var snapshot *wedding.State
		if initial {
			snapshot = weddingCeremonyLoginSnapshot(t, repos)
		}
		if err := s.runCurrentWeddingCeremonyRecovery(identity, snapshot); err != nil {
			t.Fatal(err)
		}
		assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 38, false)
	}
	s.stopCurrentWeddingCeremonyTimers()
	if len(s.wedding.ceremonyLogins) != 0 {
		t.Fatal("service stop retained login session references")
	}
}

func TestWeddingCeremonyLoginWithoutReadyNeverArmsLaterSceneRecovery(t *testing.T) {
	s, identity, repos := weddingCeremonyLoginFixture(t, false)
	initial := weddingCeremonyLoginSnapshot(t, repos)
	if err := s.runCurrentWeddingCeremonyRecovery(identity, initial); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 38, false)
	// This login subsequently attends the ceremony and encounters a full bag.
	// Persisting readiness must not turn its later voluntary exit into a login.
	for _, id := range []string{"29", "30"} {
		c, _, _ := repos.Character.Load(context.Background(), id)
		c.Stats["wedding_v1_ceremony_ready_at"] = c.Stats["wedding_v1_ceremony_at"]
		if err := r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	for _, snapshot := range []*wedding.State{nil, weddingCeremonyLoginSnapshot(t, repos)} {
		if err := s.runCurrentWeddingCeremonyRecovery(identity, snapshot); err != nil {
			t.Fatal(err)
		}
		assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 38, false)
	}
	// A genuinely new binding may restore the durable admission once.
	identity.session.characterGeneration++
	replacement, _ := s.boundGameSessionCharacterSnapshot(identity.session)
	if err := s.runCurrentWeddingCeremonyRecovery(replacement, weddingCeremonyLoginSnapshot(t, repos)); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, replacement, repos, 39, false)
	if err := s.runCurrentWeddingCeremonyRecovery(identity, initial); err != wedding.ErrOwnership {
		t.Fatalf("old binding could recreate login recovery: %v", err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, replacement, repos, 39, false)
}

func TestWeddingCeremonyPendingLoginWaitsForSceneReadiness(t *testing.T) {
	s, identity, repos := weddingCeremonyLoginFixture(t, true)
	identity.session.townSceneReadyCharacterID = 0
	if err := s.runCurrentWeddingCeremonyRecovery(identity, weddingCeremonyLoginSnapshot(t, repos)); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 38, true)
	if err := s.runCurrentWeddingCeremonyRecovery(identity, nil); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 38, true)
	identity.session.townSceneReadyCharacterID = identity.character
	if err := s.runCurrentWeddingCeremonyRecovery(identity, nil); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 39, false)
}

func TestWeddingCeremonyDeferredInitialUsesCapturedAdmission(t *testing.T) {
	s, identity, repos := weddingCeremonyLoginFixture(t, false)
	initial := weddingCeremonyLoginSnapshot(t, repos)
	// A later scene event earns admission before the deferred initial callback.
	// The callback still registers the initial ReadyAt=0 snapshot as consumed.
	for _, id := range []string{"29", "30"} {
		c, _, _ := repos.Character.Load(context.Background(), id)
		c.Stats["wedding_v1_ceremony_ready_at"] = c.Stats["wedding_v1_ceremony_at"]
		if err := r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.runCurrentWeddingCeremonyRecovery(identity, initial); err != nil {
		t.Fatal(err)
	}
	assertWeddingCeremonyLoginRecovery(t, s, identity, repos, 38, false)
}

func TestWeddingCeremonyFullGiftInventoryRetriesWithoutSceneReentry(t *testing.T) {
	s, first, second, _ := weddingRoomLeaseFixture(t)
	defer s.stopCurrentWeddingCeremonyTimers()
	table, err := town.Load(context.Background(), townMoveTestSource{
		"town/town.lst":   "39 `church.twn`",
		"town/church.twn": "[area]\n8 `Church/Church.map` `[normal]`\n[/area]",
	}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	now := time.Unix(1793534400, 0)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	repos := memory.NewMemoryGroup()
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.wedding.rules.Ceremonies = map[byte]wedding.Ceremony{0: {Kind: 0}}
	s.wedding.rules.CeremonyGifts = map[byte]wedding.Gift{0: {ItemID: 10002629, Count: 1}}
	s.wedding.rules.CeremonySeconds = 60
	source := cargoQuerySource()
	source["stackable/stackable.lst"] += "\n10002629 `weddinggift.stk`\n"
	source["stackable/weddinggift.stk"] = "[stackable type]\n`[booster]` 0\n[stack limit]\n1\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	definition, err := catalog.ResolveItem(10002629)
	if err != nil {
		t.Fatal(err)
	}
	placementSlots := currentPVFItemPlacementPolicy(definition).ForGrant().EmptySlots()
	if len(placementSlots) == 0 {
		t.Fatal("gift has no real inventory destination")
	}
	pair := weddingCeremonyRecords()
	for _, c := range []*r.CharacterRecord{&pair.Actor, &pair.Partner} {
		c.Stats["wedding_v1_ceremony_at"] = now.Unix()
		c.Stats["wedding_v1_ceremony_channel"] = int64(first.channel.ID)
		c.Stats["town_id"], c.Stats["area_id"] = 39, 8
		if err := r.SaveCharacterFields(context.Background(), repos.Character, *c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		bag := r.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]r.ItemStack{}}
		if c.CharacterID == "30" {
			for _, slot := range placementSlots {
				bag.Slots["0:"+strconv.Itoa(int(slot))] = r.ItemStack{ItemID: 100, Count: 1}
			}
		}
		if err := repos.Inventory.Save(context.Background(), bag); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*gameSession{first, second} {
		p.setFinishLoadingGates(finishLoadingGates{State: true, Completion: true, PostFinish: true})
		p.townSceneReadyCharacterID = p.selectedCharacterID
		p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 39, AreaID: 8}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, AccountID: p.accountID, TownID: 39, AreaID: 8, ChannelID: first.channel.ID, Instance: weddingPairInstance(29, 30), Session: p})
	}
	key, _ := currentWeddingCeremonyKey(pair.Actor, pair.Partner)
	ceremony := &currentWeddingCeremony{first: wedding.Actor{AccountID: pair.Actor.AccountID, CharacterID: "29"}, second: wedding.Actor{AccountID: pair.Partner.AccountID, CharacterID: "30"}, at: now.Unix(), route: wedding.CeremonyRoute{Channel: byte(first.channel.ID), Area: 8}}
	s.wedding.ceremonies = map[string]*currentWeddingCeremony{key: ceremony}
	a, af := s.onlineGameSessionCharacterSnapshot(29)
	b, bf := s.onlineGameSessionCharacterSnapshot(30)
	if !af || !bf || !s.currentWeddingChurchPresence(a, b, ceremony.route) {
		t.Fatalf("test actors not fully present: %t %t", af, bf)
	}
	s.runCurrentWeddingCeremony(key, ceremony)
	if ceremony.timer == nil {
		c, _, _ := repos.Character.Load(context.Background(), "29")
		state, readErr := wedding.Read(c)
		t.Fatalf("admitted ceremony did not arm completion timer: %+v stateErr=%v failure=%q", state, readErr, ceremony.lastFailure)
	}
	ceremony.timer.Stop() // Invoke the next owned callback with a controlled clock.
	admittedAt := now.Unix()
	assertAdmittedWithoutGifts := func() {
		t.Helper()
		for _, id := range []string{"29", "30"} {
			c, _, _ := repos.Character.Load(context.Background(), id)
			state, readErr := wedding.Read(c)
			if readErr != nil || state.Phase != wedding.InCeremony || state.CeremonyReadyAt != admittedAt {
				t.Fatalf("durable admission lost for %s: %+v %v", id, state, readErr)
			}
			bag, _, _ := repos.Inventory.Load(context.Background(), id)
			assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 0)
			box, _, _ := repos.Mailbox.Load(context.Background(), id)
			if len(box.Mails) != 0 {
				t.Fatal("gift mail committed before completion or after failed completion")
			}
		}
	}
	assertAdmittedWithoutGifts()
	// A durable admission survives leaving the scene. No replacement loading
	// tail or second entry is used by any subsequent completion attempt.
	for _, p := range []*gameSession{first, second} {
		s.onlinePlayers.LeaveArea(p.selectedCharacterID)
		p.setFinishLoadingGates(finishLoadingGates{})
		delete(s.gameSessions, p.selectedCharacterID)
	}
	now = now.Add(59 * time.Second)
	s.runCurrentWeddingCeremony(key, ceremony)
	assertAdmittedWithoutGifts()
	if ceremony.timer == nil {
		t.Fatal("completion was not retained until the PVF interval elapsed")
	}
	ceremony.timer.Stop()
	now = now.Add(time.Second)
	// Reuse the same second-mail fault as the delivery transaction regression:
	// actor 29's bag grant must roll back when actor 30's full-bag mail fails.
	unit := repos.CharacterTrade
	repos.CharacterTrade = weddingDeliveryFaultUnit{unit, unit.(r.WeddingLifecycleUnitOfWork)}
	s.runCurrentWeddingCeremony(key, ceremony)
	assertAdmittedWithoutGifts()
	if ceremony.timer == nil {
		t.Fatal("mail failure did not retain a bounded retry")
	}
	ceremony.timer.Stop()
	repos.CharacterTrade = unit
	s.runCurrentWeddingCeremony(key, ceremony)
	assertCompletedGifts := func() {
		t.Helper()
		for _, id := range []string{"29", "30"} {
			c, _, _ := repos.Character.Load(context.Background(), id)
			state, _ := wedding.Read(c)
			if state.Phase != wedding.Married {
				t.Fatalf("admitted offline completion did not resume %s: %+v failure=%q", id, state, ceremony.lastFailure)
			}
			bag, _, _ := repos.Inventory.Load(context.Background(), id)
			box, _, _ := repos.Mailbox.Load(context.Background(), id)
			if id == "29" {
				assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 1)
				if len(box.Mails) != 0 {
					t.Fatal("bag recipient also received duplicate mail")
				}
			} else {
				assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 0)
				if len(box.Mails) != 1 {
					t.Fatalf("full bag lost or duplicated fallback mail: %+v", box)
				}
				for _, mail := range box.Mails {
					if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 10002629 || mail.Attachments[0].Count != 1 || mail.Attachments[0].Extra["pvf_path"] == "" {
						t.Fatal("mail lost real gift identity")
					}
				}
			}
		}
	}
	assertCompletedGifts()
	if _, retained := s.wedding.ceremonies[key]; retained {
		t.Fatal("successful retry retained timer")
	}
	s.runCurrentWeddingCeremony(key, ceremony)
	assertCompletedGifts()
}

func TestWeddingCeremonyNamesAlwaysFollowNativeGroomBrideSlots(t *testing.T) {
	pair := weddingCeremonyRecords()
	for _, p := range []wedding.PairResult{pair, {Actor: pair.Partner, Partner: pair.Actor}} {
		groom, bride, err := currentWeddingCeremonyNames(p.Actor, p.Partner)
		if err != nil || string(groom) != "groom" || string(bride) != "bride" {
			t.Fatalf("recipient order changed movie actors: %q %q %v", groom, bride, err)
		}
	}
	pair.Actor.Job = "unknown"
	if _, _, err := currentWeddingCeremonyNames(pair.Actor, pair.Partner); err != wedding.ErrEligibility {
		t.Fatalf("unknown gender accepted: %v", err)
	}
}

func TestWeddingCeremonyExpiredReturnUsesVerifiedPreviousTownOrPVFGate(t *testing.T) {
	s, _, _ := newTownMoveTest(t)
	c := r.CharacterRecord{CharacterID: "29", Stats: map[string]int64{
		currentTownPrevVillageTownStat: 38, currentTownPrevVillageAreaStat: 0,
		currentTownPrevVillagePosXStat: 1234, currentTownPrevVillagePosYStat: 210, currentTownPrevVillageDirectionStat: 2,
	}}
	p, err := s.currentWeddingCeremonyReturn(c)
	if err != nil || p.Town != 38 || p.Area != 0 || p.X != 1234 || p.Y != 210 {
		t.Fatalf("verified prior town not preserved: %+v %v", p, err)
	}
	delete(c.Stats, currentTownPrevVillageTownStat)
	p, err = s.currentWeddingCeremonyReturn(c)
	if err != nil || p.Town != 38 || p.Area != 1 || p.X != 450 || p.Y != 234 {
		t.Fatalf("actual PVF login gate not used: %+v %v", p, err)
	}
	s.townCatalog = nil
	if _, err := s.currentWeddingCeremonyReturn(c); err == nil {
		t.Fatal("fabricated return coordinates without town catalog")
	}
}

func TestWeddingCeremonyChurchRequiresBothCommittedTownScenes(t *testing.T) {
	s, first, second, _ := weddingRoomLeaseFixture(t)
	table, err := town.Load(context.Background(), townMoveTestSource{
		"town/town.lst":   "39 `church.twn`",
		"town/church.twn": "[area]\n8 `Church/Church.map` `[normal]`\n[/area]",
	}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	route := wedding.CeremonyRoute{Channel: byte(first.channel.ID), Area: 8}
	for _, p := range []*gameSession{first, second} {
		p.townSceneReadyCharacterID = p.selectedCharacterID
		p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 39, AreaID: 8}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, AccountID: p.accountID, TownID: 39, AreaID: 8, ChannelID: first.channel.ID, Instance: weddingPairInstance(29, 30), Session: p})
	}
	a, _ := s.boundGameSessionCharacterSnapshot(first)
	b, _ := s.boundGameSessionCharacterSnapshot(second)
	if !s.currentWeddingChurchPresence(a, b, route) {
		t.Fatal("committed native town scenes rejected without dungeon loading gates")
	}
	for _, gates := range []finishLoadingGates{{}, {State: true}, {State: true, Completion: true}, {Completion: true, PostFinish: true}, {State: true, PostFinish: true}} {
		second.setFinishLoadingGates(gates)
		if !s.currentWeddingChurchPresence(a, b, route) {
			t.Fatalf("ordinary town transition incorrectly requires dungeon gates: %+v", gates)
		}
	}
	second.townSceneReadyCharacterID = 0
	if s.currentWeddingChurchPresence(a, b, route) {
		t.Fatal("uncommitted town scene accepted")
	}
	second.townSceneReadyCharacterID = second.selectedCharacterID
	second.confirmedDungeonReturnStatePending = true
	if s.currentWeddingChurchPresence(a, b, route) {
		t.Fatal("retained dungeon return accepted")
	}
	second.confirmedDungeonReturnStatePending = false
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, TownID: 39, AreaID: 8, ChannelID: first.channel.ID, Instance: 0, Session: second})
	if s.currentWeddingChurchPresence(a, b, route) {
		t.Fatal("other Church instance accepted")
	}
}

func TestWeddingCeremonyTimerVersionAndStopFenceOldCallbacks(t *testing.T) {
	s := &Service{}
	result := weddingCeremonyRecords()
	defer s.stopCurrentWeddingCeremonyTimers()
	if err := s.scheduleCurrentWeddingCeremonyLocked(result); err != nil {
		t.Fatal(err)
	}
	key, _ := currentWeddingCeremonyKey(result.Actor, result.Partner)
	old := s.wedding.ceremonies[key]
	if err := s.scheduleCurrentWeddingCeremonyLocked(result); err != nil || s.wedding.ceremonies[key] != old {
		t.Fatalf("same reservation duplicated its timer: %v", err)
	}
	result.Actor.Stats["wedding_v1_ceremony_at"]++
	result.Partner.Stats["wedding_v1_ceremony_at"]++
	if err := s.scheduleCurrentWeddingCeremonyLocked(result); err != nil {
		t.Fatal(err)
	}
	replacement := s.wedding.ceremonies[key]
	if replacement == old {
		t.Fatal("new reservation reused old timer")
	}
	s.runCurrentWeddingCeremony(key, old)
	if s.wedding.ceremonies[key] != replacement || replacement.timer == nil {
		t.Fatal("stale timer consumed replacement")
	}
	s.stopCurrentWeddingCeremonyTimers()
	s.runCurrentWeddingCeremony(key, replacement)
	if err := s.scheduleCurrentWeddingCeremonyLocked(result); err != wedding.ErrUnavailable {
		t.Fatalf("queued recovery after service stop accepted: %v", err)
	}
	if len(s.wedding.ceremonies) != 0 {
		t.Fatal("stopped timer restored ceremony")
	}
}

func TestWeddingCeremonyChurchGateDoesNotAllowOtherAreasOrMarriedReentry(t *testing.T) {
	c := weddingCeremonyRecords().Actor
	if !currentWeddingChurchMoveAllowed(c, 39, 8) || currentWeddingChurchMoveAllowed(c, 39, 9) || currentWeddingChurchMoveAllowed(c, 38, 8) {
		t.Fatal("reservation route not enforced")
	}
	c.Stats["wedding_v1_phase"] = int64(wedding.Married)
	c.Stats["town_id"], c.Stats["area_id"] = 39, 8
	if !currentWeddingChurchMoveAllowed(c, 39, 8) {
		t.Fatal("newly married participant cannot finish its current scene")
	}
	c.Stats["town_id"] = 38
	if currentWeddingChurchMoveAllowed(c, 39, 8) {
		t.Fatal("married participant reentered a finished ceremony")
	}
}
