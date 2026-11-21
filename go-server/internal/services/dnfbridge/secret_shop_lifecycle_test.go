package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/secretshop"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestSecretShopRefusedRetryRestoresFrozenMerchant(t *testing.T) {
	t.Setenv("DNFBRIDGE_BLACK_DIAMOND_SECRET_SHOP", "on")
	s, old, session, conn := prepareOrdinaryCompletedSettlementRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	old.settlementPlayResultReceived, old.settlementStatisticReceived = true, true
	markCurrentDungeonCardRewardCommittedForTest(old)
	authenticateRound100RetrySession(s, session)
	old.townReturnOrigin = currentTownPositionSnapshot{CharacterID: 99, TownID: 38, AreaID: 1, PositionValid: true}
	scene, _ := old.Session.Scene()
	e := &currentSecretShopEncounter{NPCID: 1003, Nonce: [2]int64{987, 654}, Members: map[uint16]bool{99: false}}
	old.secretShopEncounter, old.secretShopChecked = e, true
	old.secretShop = &currentSecretShop{Encounter: e, Scene: scene, Sent: true, Rows: []currentSecretShopRow{{Candidate: secretshop.Candidate{ItemID: 900001, Count: 4}, Remaining: 2}}}
	repos, _ := s.repositoryProvider()
	c, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	c.Stats["fatigue"], c.Stats["fatigue_limit"], c.Stats["fatigue_used"], c.Stats["fatigue_display_update"] = 0, 156, 156, 156
	c.Stats[fatigue.DayKey] = premium.ServiceDayID(s.gameplayNow())
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := s.retryCurrentCompletedDungeon(session, old, "secret_shop_refused_retry"); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != old || !s.currentCompletedDungeonActionReady(session, old) {
		t.Fatal("refusal did not restore completed owner")
	}
	assertSecretShopNPCs(t, s, conn, 1000, 1003)
	if old.secretShopEncounter != e || old.secretShop.Encounter != e || old.secretShop.Rows[0].Remaining != 2 || !old.secretShop.Sent || old.secretShopRetryPending {
		t.Fatal("refusal changed frozen merchant or stock")
	}
}

func TestRealSecretShopPVFMapExclusions(t *testing.T) {
	f, r, _ := realSecretShopFixture(t)
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"dungeon/legend/ekern/warriors/warriors.dgn", false},
		{"dungeon/legend/ekern/warriors/cdnf_party_warriors.dgn", false},
		{"dungeon/legend/ekern/warriors/cdnf_legend_dummy.dgn", false},
		{"dungeon/impossible/dimension.dgn", false},
		{"dungeon/dimensioncrack/blackearth/7168_blackearth.dgn", false},
		{"dungeon/dimensioncrack/bakalcastle/7174_bakalcastle_r_1st.dgn", false},
		{"dungeon/event/2018_jumping/dimension_tutorialmode.dgn", false},
		{"dungeon/infernomode/inferno_powerstation2.dgn", false},
		{"dungeon/towers/grief/5115_001.dgn", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			text, err := f.shop.items.source.ReadText(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := dnfpvf.Parse(tc.path, text)
			if err != nil {
				t.Fatal(err)
			}
			r.Dungeon = worldmap.ParseDungeon(7146, tc.path, doc)
			if got := currentSecretShopEligible(r); got != tc.allowed {
				t.Fatalf("Gabriel eligibility=%v want=%v flags=%v", got, tc.allowed, r.Dungeon.Metadata.Flags)
			}
		})
	}
	// Ancient is not a blanket exclusion: its own NPC table can select 1004.
	r.Dungeon.Metadata = worldmap.DungeonMetadata{}
	for _, flags := range []map[string]bool{nil, {"ancient dungeon": true}} {
		r.Dungeon.Metadata.Flags = flags
		if !currentSecretShopEligible(r) {
			t.Fatal("ordinary/ancient dungeon was excluded")
		}
	}
}

func assertSecretShopNPCs(t *testing.T, s *Service, c *bufferConn, want ...uint32) {
	t.Helper()
	packets := shopPackets(t, s, c, 279)
	if len(packets) != len(want) {
		t.Fatalf("NPC notices=%d want=%v", len(packets), want)
	}
	for i, body := range packets {
		if len(body) != 4 || binary.LittleEndian.Uint32(body) != want[i] {
			t.Fatalf("NPC notice %d=%x want=%d", i, body, want[i])
		}
	}
}

func TestRealSecretShopDefaultEncounterResetsOnceWithoutReroll(t *testing.T) {
	f, r, _ := realSecretShopFixture(t)
	r.secretShopChecked = true
	r.secretShopEncounter = &currentSecretShopEncounter{NPCID: secretshop.DefaultNPCID, Members: map[uint16]bool{19: false}}
	f.service.dungeonChoice = func(int) (int, error) { t.Fatal("frozen default encounter rerolled"); return 0, nil }
	scene, _ := r.Session.Scene()
	for i := 0; i < 3; i++ {
		if err := f.service.offerCurrentSecretShopLocked(f.session, r, scene); err != nil {
			t.Fatal(err)
		}
	}
	assertSecretShopNPCs(t, f.service, f.connection, 1000)
	if len(shopPackets(t, f.service, f.connection, 280)) != 0 {
		t.Fatal("default NPC received a Gabriel shelf")
	}
}

func TestSecretShopRetiresBeforeRetrySelectorOrReplacement(t *testing.T) {
	for _, route := range []string{"selector", "retry", "replacement"} {
		for _, fail := range []bool{false, true} {
			name := route + "/success"
			if fail {
				name = route + "/write_failure"
			}
			t.Run(name, func(t *testing.T) {
				s, old, session, conn, _ := prepareCompletedSettlementRuntime(t)
				old.settlementClearRewardSent = true
				old.settlementPlayResultReceived = true
				old.settlementStatisticReceived = true
				old.settlementPhase = currentDungeonSettlementPhaseResultShown
				old.townReturnOrigin = currentTownPositionSnapshot{CharacterID: 99, TownID: 7, AreaID: 3, PositionValid: true}
				old.secretShop = &currentSecretShop{Encounter: &currentSecretShopEncounter{NPCID: 1003}, Sent: true, Open: true}
				conn.write.Reset()
				injected := errors.New("reset NPC write failed")
				if fail {
					session.conn = &failNthDungeonWriteConn{failAt: 1, err: injected}
				}
				var err error
				if route == "replacement" {
					_, next := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{singleMonster: true})
					err = s.commitDungeonRuntimeWithPetClock(session, next, false)
					if err == nil && session.dungeon.runtime != next {
						t.Fatal("replacement not committed")
					}
				} else {
					var retired bool
					retired, err = s.retireCompletedDungeonForTownAction(session, route == "retry", old)
					if err == nil && (!retired || session.dungeon.runtime != nil) {
						t.Fatal("completed runtime not retired")
					}
				}
				if fail {
					if !errors.Is(err, injected) || session.dungeon.runtime != old || !old.secretShop.Sent || !old.secretShop.Open {
						t.Fatalf("failed reset lost old owner/state: error=%v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				assertSecretShopNPCs(t, s, conn, 1000)
				if old.secretShop.Sent || old.secretShop.Open {
					t.Fatal("retired shop remains usable")
				}
				if err := s.clearCurrentSecretShopLocked(session, old); err != nil {
					t.Fatal(err)
				}
				assertSecretShopNPCs(t, s, conn, 1000)
			})
		}
	}
}

func TestRealSecretShopShelfFailureStillClearsVisibleNPC(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	scene, _ := r.Session.Scene()
	e := &currentSecretShopEncounter{NPCID: 1003, Nonce: [2]int64{123, 456}, Members: map[uint16]bool{19: false}}
	var err error
	r.secretShop, err = f.service.buildCurrentSecretShop(catalog, e, r, scene, false)
	if err != nil || r.secretShop == nil {
		t.Fatal("shop fixture", err)
	}
	injected := errors.New("shelf failed after NPC was written")
	c := &failNthDungeonWriteConn{failAt: 2, err: injected}
	f.session.conn = c
	if err := f.service.sendCurrentSecretShopLocked(f.session, r); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	assertSecretShopNPCs(t, f.service, &c.bufferConn, 1003)
	if err := f.service.clearCurrentSecretShopLocked(f.session, r); err != nil {
		t.Fatal(err)
	}
	assertSecretShopNPCs(t, f.service, &c.bufferConn, 1003, 1000)
}

func TestRealSecretShopRepeatedWriteFailureKeepsFrozenRetry(t *testing.T) {
	f, r, _ := realSecretShopFixture(t)
	r.secretShopEncounter = &currentSecretShopEncounter{NPCID: 1000, Members: map[uint16]bool{19: false}}
	r.secretShopChecked = true
	r.secretShopRetryPending = true
	scene, _ := r.Session.Scene()
	c := &failNthDungeonWriteConn{err: errors.New("default NPC write failed"), failWhen: func([]byte) bool { return true }}
	f.session.conn = c
	for i := 0; i < 2; i++ {
		f.service.retryPendingCurrentSecretShopLocked(f.session, r, scene)
		if !r.secretShopRetryPending {
			t.Fatal("failed retry was discarded")
		}
	}
	c.failWhen = nil
	f.service.retryPendingCurrentSecretShopLocked(f.session, r, scene)
	if r.secretShopRetryPending {
		t.Fatal("successful retry remains pending")
	}
	assertSecretShopNPCs(t, f.service, &c.bufferConn, 1000)
}
