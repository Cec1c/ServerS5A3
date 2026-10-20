package dnfbridge

import "testing"

func TestSingleAntonCardPolicyKeepsOrdinaryAndLukeRewards(t *testing.T) {
	for _, kind := range []string{"ordinary", "luke"} {
		t.Run(kind, func(t *testing.T) {
			f := review452GroundRun(t, kind, kind == "luke", false)
			installCurrentTournamentCardRewardTestPVF(t, f.service)
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
	installCurrentTournamentCardRewardTestPVF(t, f.service)
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
