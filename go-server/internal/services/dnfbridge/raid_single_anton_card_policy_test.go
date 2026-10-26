package dnfbridge

import "testing"

// Paid-card selection reads its level-indexed cost table as well as the reward
// table. Keep both inputs explicit in this policy fixture.
func installSingleAntonCardPolicyTestPVF(t *testing.T, service *Service) {
	t.Helper()
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source, ok := catalog.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("monster catalog source type=%T", catalog.source)
	}
	source[currentDungeonClearRewardRulePath] = "[gold card cost table]\n1 100\n20 100\n[/gold card cost table]\n"
	installCurrentTournamentCardRewardTestPVF(t, service)
}

func TestSingleAntonCardPolicyKeepsOrdinaryAndLukeRewards(t *testing.T) {
	for _, kind := range []string{"ordinary", "luke"} {
		t.Run(kind, func(t *testing.T) {
			f := review452GroundRun(t, kind, kind == "luke", false)
			installSingleAntonCardPolicyTestPVF(t, f.service)
			if err := f.service.freezeCurrentDungeonCardRewardStateLocked(f.session, f.runtime); err != nil {
				t.Fatal(err)
			}
			if f.runtime.settlementCardRewardState == nil {
				t.Fatal("unrelated ordinary reward plan was suppressed")
			}
		})
	}
}

func TestSingleAntonDoesNotOpenOrdinaryCardFlow(t *testing.T) {
	f := review452GroundRun(t, "anton", true, false)
	installSingleAntonCardPolicyTestPVF(t, f.service)
	r := f.runtime
	r.settlementClearRewardSent = true
	r.settlementPhase = currentDungeonSettlementPhaseResultShown
	if err := f.service.freezeCurrentDungeonCardRewardStateLocked(f.session, r); err != nil {
		t.Fatal(err)
	}
	if r.settlementCardRewardState != nil {
		t.Fatal("single Anton created an ordinary reward plan alongside its raid reward flow")
	}
	for i := 0; i < 2; i++ {
		if err := f.service.sendCurrentDungeonCardScrollStateLocked(f.session, r, "single_anton_test"); err != nil {
			t.Fatal(err)
		}
		if err := f.service.sendCurrentDungeonCardLayoutLocked(f.session, r, "single_anton_test"); err != nil {
			t.Fatal(err)
		}
	}
	if r.settlementCardScrollStateSent || r.settlementCardLayoutSent || f.session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("single Anton opened or replayed the ordinary card animation")
	}
}
