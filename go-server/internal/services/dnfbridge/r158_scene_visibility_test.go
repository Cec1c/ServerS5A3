package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Real PVF, memory identities and raid/town presence. Invitations, entry and
// loading confirmations use real handlers; no native actor or GUI is simulated.
func TestR158FiendWarTwoMemberVisibilitySequence(t *testing.T) {
	for _, area := range []int{30, 31} {
		t.Run(fmt.Sprint(area), func(t *testing.T) {
			oldDispatch := partyDungeonLoadingBarrierDispatch
			partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
			defer func() { partyDungeonLoadingBarrierDispatch = oldDispatch }()
			f, peers := r134N1Fixture(t)
			s, b := f.service, f.state.Battle
			trace := &partyDungeonSyncTrace{}
			for _, p := range peers {
				p.conn.(*partyDungeonSyncConn).trace = trace
			}
			r134N1Invite(t, f, peers)
			r134N1Send(t, s, peers[1], dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(peers[0].selectedCharacterID, 0, 0))
			for _, p := range peers {
				if s.runtimePartyStateForSession(p).PartyID != 0 {
					t.Fatal("manual FiendWar party was created before encounter")
				}
			}
			b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = 1
			b.FiendWar.Symbols[b.FiendWar.Config.Symbols["bot boss state"]] = 1
			syncCurrentFiendWarDungeons(b)
			for _, p := range peers {
				r134N1Presence(t, f, p, area)
			}
			id := b.FiendWar.Config.Areas[area].Dungeon
			if err := s.tryCurrentFiendWarEncounter(peers[0]); err != nil {
				t.Fatal(err)
			}
			for _, p := range peers {
				if p.dungeon.runtime == nil {
					t.Fatal("entry missing", f.logText(t))
				}
			}
			order := []int{0, 1}
			if area == 31 {
				order = []int{1, 0}
			}
			for _, i := range order {
				if err := s.handleLegacyGamePacket(peers[i], buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), 37, 0, make([]byte, 8))); err != nil {
					t.Fatal(err)
				}
			}
			if out := os.Getenv("R158_SCENE_EVIDENCE"); out != "" {
				if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("phase-%d.log", area)), []byte(f.logText(t)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range peers {
				t.Logf("char=%d postStart=%v finishState=%v completion=%v postFinish=%v pendingPlacement=%v keyValid=%v", p.selectedCharacterID, p.postStartMapPlayerStateSent, p.currentFinishLoadingStateSent, p.currentFinishLoadingCompletionSent, p.postFinishLoadingPlayerStateSent, p.pendingPostStartMapPlacement != nil, p.dungeonPartyRunValid)
			}
			steps := trace.orderedSteps(t)
			for i, p := range peers {
				scene, ok := p.dungeon.runtime.Session.Scene()
				if !ok {
					t.Fatal("scene absent")
				}
				want := int64(306000461)
				if area == 31 {
					want = 306000458
				}
				if scene.Map.Map.ID != want {
					t.Fatalf("map=%d want=%d", scene.Map.Map.ID, want)
				}
				if !peerSceneStableForPeerActorLandingResend(p) || !p.postStartMapPlayerStateSent {
					t.Fatalf("server tail incomplete char=%d", p.selectedCharacterID)
				}
				last120 := -1
				for j, st := range steps {
					if st.CharID == p.selectedCharacterID && st.Classification == 0 && st.MsgID == 120 {
						last120 = j
					}
				}
				peerKey := currentSceneActorObjectKey(peers[1-i].selectedCharacterID)
				modes := map[byte]int{}
				op9 := 0
				for j, st := range steps {
					if j <= last120 || st.CharID != p.selectedCharacterID || st.Classification != 0 {
						continue
					}
					// mode0 raw71 and mode1 raw16 precede their object key.
					offset := 76
					if len(st.Body) > 0 && st.Body[0] == 1 {
						offset = 21
					}
					if st.MsgID == 2 && len(st.Body) >= offset+2 && st.Body[0] <= 1 && binary.LittleEndian.Uint16(st.Body[offset:offset+2]) == peerKey {
						modes[st.Body[0]]++
					}
					if st.MsgID == 9 && len(st.Body) > 9 && binary.LittleEndian.Uint16(st.Body[4:6]) == peerKey {
						op9++
					}
				}
				t.Logf("area=%d dungeon=%d receiver=%d map=%d last120=%d postTailPeerModes=%v op9=%d packetCount=%d", area, id, p.selectedCharacterID, want, last120, modes, op9, len(steps))
				if last120 < 0 || modes[0] == 0 || modes[1] == 0 || op9 == 0 {
					t.Errorf("peer projection missing after local placement receiver=%d", p.selectedCharacterID)
				}
			}
			if area == 30 {
				r158PreparedOwnerBoundaries(t, f, peers, trace)
			}

		})
	}
}

// These controls use the internal handler shape used by the actual barrier.
func r158PreparedOwnerBoundaries(t *testing.T, f *raidStartGuardFixture, peers []*gameSession, trace *partyDungeonSyncTrace) {
	s, p := f.service, peers[1]
	r := p.dungeon.runtime
	b := f.state.Battle
	prepared := runtimePartyPreparedDungeonEntry{session: p, runtime: r, committed: true, request: r.Request}
	body := encodeCurrentDungeonSelectRequest(r.Request)
	if !s.currentPreparedDungeonSelectionAllowed(p, uint32(r.Dungeon.ID), &prepared) {
		t.Fatal("admitted prepared owner rejected")
	}
	for _, name := range []string{"new_client_selection", "wrong_session", "replaced_encounter", "expired_battle"} {
		t.Run(name, func(t *testing.T) {
			candidate := prepared
			arg := &candidate
			switch name {
			case "new_client_selection":
				arg = nil
			case "wrong_session":
				candidate.session = peers[0]
			case "replaced_encounter":
				area, _ := b.FiendWar.AreaForDungeon(int(r.Dungeon.ID))
				old := b.FiendWarEncounters[area.Index]
				changed := old
				changed.RunID += "-replacement"
				b.FiendWarEncounters[area.Index] = changed
				defer func() { b.FiendWarEncounters[area.Index] = old }()
			case "expired_battle":
				old := b.Deadline
				b.Deadline = time.Now().Add(-time.Second)
				defer func() { b.Deadline = old }()
			}
			before := len(trace.orderedSteps(t))
			if err := s.handleDungeonSelectUpperPlanned(p, body, arg, arg == nil, nil); err != nil {
				t.Fatal(err)
			}
			after := trace.orderedSteps(t)[before:]
			if len(after) != 1 || after[0].CharID != p.selectedCharacterID || after[0].MsgID != 16 || after[0].Classification != 1 || len(after[0].Body) == 0 || after[0].Body[0] != 0 {
				t.Fatalf("expected only refusal, got %v", after)
			}
			if p.dungeon.runtime != r {
				t.Fatal("refusal changed admitted runtime")
			}
		})
	}
}
