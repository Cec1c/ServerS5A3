package dnfbridge

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Production preloads the immutable dungeon drop catalog before listeners
// open. The observed Anton card path rebuilt that catalog from the same PVF on
// every second status tick and again for each 660, taking several seconds.
func preloadAntonRewardDropCatalog(t *testing.T, f *raidStartGuardFixture) {
	t.Helper()
	if f == nil || f.service == nil || f.service.initialEquipmentArchive == nil {
		t.Fatal("Anton reward fixture has no PVF archive")
	}
	monsters, err := newPVFDungeonMonsterCatalog(f.service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := monsters.DropCatalog(); err != nil {
		t.Fatal(err)
	}
	f.service.dungeonMonsterTable = monsters
}

func showAntonRewardFixtureCard(t *testing.T, f *raidStartGuardFixture, session *gameSession) *currentAntonRewardFlow {
	t.Helper()
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
	if flow == nil {
		t.Fatal("fixture reward flow missing")
	}
	return flow
}

func assertAntonCardWire(t *testing.T, f *raidStartGuardFixture, wantKind byte) {
	t.Helper()
	var card, ack bool
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.Classification == 0 && packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == wantKind {
			card = true
		}
		if packet.Header.Classification == 1 && packet.Header.MsgID == 660 {
			if !bytes.Equal(packet.Body, []byte{1}) {
				t.Fatalf("card request failed: body=%x", packet.Body)
			}
			ack = true
		}
	}
	if !card || !ack {
		t.Fatalf("card response incomplete: kind=%d card=%t ack=%t", wantKind, card, ack)
	}
}

func TestAntonRewardCardDeadlineDoesNotRebuildPVFCatalog(t *testing.T) {
	t.Run("automatic_selection", func(t *testing.T) {
		f, _, _ := firstAntonFlowFixture(t)
		preloadAntonRewardDropCatalog(t, f)
		f.service.initialEquipmentArchive.ReleaseReadCaches()
		flow := f.state.Battle.RewardFlow[0]
		movie := currentAntonMovieWindow(flow)
		flow.PartyShownAt = flow.CompletedAt.Add(movie)
		flow.AutoSelectAt = flow.CompletedAt.Add(2 * movie)
		flow.PartySelectAfter = nil
		deadline := currentRaidOwnerPartySelectAt(flow, 19)

		started := time.Now()
		_, errs := f.service.advanceCurrentAntonRewardFlows(deadline, zeroAntonRoll)
		elapsed := time.Since(started)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		if elapsed > 2500*time.Millisecond {
			t.Fatalf("automatic selection spent %s rebuilding PVF after the client card deadline", elapsed)
		}
		if card := flow.Party[19]; card == nil || !card.Committed {
			t.Fatal("automatic selection did not commit its frozen card")
		}
	})

	t.Run("rejected_catalog_fails_closed", func(t *testing.T) {
		f, _, _ := firstAntonFlowFixture(t)
		f.service.dungeonMonsterTable = &pvfDungeonMonsterCatalog{
			dropCatalogLoaded: true,
			dropCatalogErr:    errors.New("rejected preloaded reward catalog"),
		}
		f.service.initialEquipmentArchive = nil
		f.service.initialEquipmentLoadErr = nil
		f.service.options.pvfPath = filepath.Join(t.TempDir(), "missing-rejected-catalog.pvf")
		flow := f.state.Battle.RewardFlow[0]
		deadline := currentAntonPartySelectAt(flow, currentAntonMovieWindow(flow))

		_, errs := f.service.advanceCurrentAntonRewardFlows(deadline, zeroAntonRoll)
		if len(errs) == 0 {
			t.Fatal("rejected reward catalog did not fail closed")
		}
		if card := flow.Party[19]; card != nil && card.Committed {
			t.Fatal("rejected reward catalog committed a card")
		}
		if antonGrantedItemCount(t, f, "19", 10304529) != 0 || antonGrantedItemCount(t, f, "19", 10093974) != 0 {
			t.Fatal("rejected reward catalog granted assets")
		}
	})

	t.Run("manual_660", func(t *testing.T) {
		f, session, _ := firstAntonFlowFixture(t)
		preloadAntonRewardDropCatalog(t, f)
		f.service.initialEquipmentArchive.ReleaseReadCaches()
		showAntonRewardFixtureCard(t, f, session)
		f.conn.write.Reset()

		started := time.Now()
		dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
		elapsed := time.Since(started)
		if elapsed > 2500*time.Millisecond {
			t.Fatalf("manual 660 spent %s rebuilding PVF before its reveal/ACK", elapsed)
		}
		assertAntonCardWire(t, f, 1)
	})

	t.Run("kind0_after_commit", func(t *testing.T) {
		f, session, _ := firstAntonFlowFixture(t)
		preloadAntonRewardDropCatalog(t, f)
		showAntonRewardFixtureCard(t, f, session)
		f.conn.write.Reset()
		dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})

		// A committed card's kind0 reply only projects the authoritative empty
		// gold pool. Supplying no archive proves this reply no longer builds one
		// while the client is waiting for the last card packet.
		f.service.dungeonMonsterTable = &pvfDungeonMonsterCatalog{
			dropCatalogLoaded: true,
			dropCatalogErr:    errors.New("invalid catalog must not block kind0"),
		}
		f.service.initialEquipmentArchive = nil
		f.service.initialEquipmentLoadErr = nil
		f.service.options.pvfPath = filepath.Join(t.TempDir(), "missing-card-deadline.pvf")
		f.conn.write.Reset()
		dispatchAntonCardRequest(t, f, session, 660, []byte{0, 0})
		assertAntonCardWire(t, f, 0)
	})
}
