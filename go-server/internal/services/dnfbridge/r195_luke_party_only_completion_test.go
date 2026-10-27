package dnfbridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func r195WantOwners(owners []uint16, want ...uint16) bool {
	if len(owners) != len(want) {
		return false
	}
	for i := range want {
		if owners[i] != want[i] {
			return false
		}
	}
	return true
}

type r195LukeCompletionPacket struct {
	Type    byte   `json:"type"`
	Op      uint16 `json:"op"`
	BodyHex string `json:"bodyhex"`
}

// Read the actual encoded connection stream, retaining only reward/state
// notifications. These are also the credential-free native fixture payloads.
func r195LukeCompletionWire(t *testing.T, stream []byte) []r195LukeCompletionPacket {
	t.Helper()
	var packets []r195LukeCompletionPacket
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if p.Header.Classification == 0 && (p.Header.MsgID == 587 || p.Header.MsgID == 600) {
			packets = append(packets, r195LukeCompletionPacket{Type: 0, Op: p.Header.MsgID, BodyHex: hex.EncodeToString(p.Body)})
		}
	}
	return packets
}

func r195LukeWantCompletionWire(t *testing.T, packets []r195LukeCompletionPacket, want ...string) {
	t.Helper()
	var got []string
	for _, p := range packets {
		body, err := hex.DecodeString(p.BodyHex)
		if err != nil || len(body) < 2 {
			t.Fatalf("invalid reward wire: %+v", p)
		}
		if p.Op == 587 {
			got = append(got, "state"+strconv.Itoa(int(body[0])))
			if body[1] != 0 {
				t.Fatalf("unexpected state argument: %+v", p)
			}
		} else if p.BodyHex == "0300" {
			got = append(got, "empty3")
		} else {
			if body[0] != 1 || len(parsePhase0CardOwners(t, body)) == 0 {
				t.Fatalf("expected actual nonempty party list: %+v", p)
			}
			got = append(got, "kind1")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reward order=%v want=%v packets=%+v", got, want, packets)
	}
}

func r195LukeCompletedPartyFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, *currentAntonRewardFlow) {
	t.Helper()
	f, session, at := normalLukeFixture(t)
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, session, id, at)
			at = at.Add(time.Second)
		}
	}
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || flow.squadRequired() || len(flow.Recipients) != 2 {
		t.Fatal("missing actual Luke party-only flow")
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	return f, session, flow
}

func r195LukeAssertNoSquadAssets(t *testing.T, f *raidStartGuardFixture, flow *currentAntonRewardFlow) {
	t.Helper()
	if len(flow.Squad) != 0 || flow.squadRequired() {
		t.Fatal("UI completion created/required a squad reward")
	}
	for _, owner := range flow.Recipients {
		if card := flow.Party[owner.CharacterID]; card == nil || !card.Committed {
			t.Fatal("actual party reward not committed", owner.CharacterID)
		}
		character, found, err := f.repos.Character.Load(context.Background(), strconv.Itoa(int(owner.CharacterID)))
		if err != nil || !found {
			t.Fatal("load rewarded character", found, err)
		}
		prefix := "raid_reward_v1_2_" + flow.InstanceID + "_0_"
		if character.Stats[prefix+"1"] == 0 || character.Stats[prefix+"3"] != 0 {
			t.Fatal("party receipt missing or squad receipt created", owner.CharacterID)
		}
	}
}

// One actual PVF fixture for automatic completion, reconnect, whole-display
// dedupe and projection-only negative mode boundaries. No async fanout is used.
func TestR195LukePartyOnlyCompletionWire(t *testing.T) {
	f, session, flow := r195LukeCompletedPartyFixture(t)
	exported := map[string][]r195LukeCompletionPacket{}
	send := func(name string, s *gameSession) []r195LukeCompletionPacket {
		t.Helper()
		f.conn.write.Reset()
		if err := f.service.sendCurrentAntonRewardFlowProjection(s, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		packets := r195LukeCompletionWire(t, f.conn.write.Bytes())
		exported[name] = packets
		return packets
	}
	r195LukeWantCompletionWire(t, send("uncommitted", session), "state4", "kind1")
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	r195LukeAssertNoSquadAssets(t, f, flow)
	// Warm persisted container normalization before comparing projection assets.
	if err := f.service.sendSelectedCurrentContainerListsRefresh(session, "r195_luke_completion_fixture"); err != nil {
		t.Fatal(err)
	}
	assets := lukeDisplayAssetSnapshot(t, f.service, session)
	r195LukeWantCompletionWire(t, send("committed", session), "kind1", "empty3", "state5")
	r195LukeWantCompletionWire(t, send("duplicate", session))
	if f.conn.write.Len() != 0 {
		t.Fatal("duplicate completion refreshed unrelated containers")
	}
	if after := lukeDisplayAssetSnapshot(t, f.service, session); !reflect.DeepEqual(assets, after) {
		t.Fatal("completion projection changed committed assets")
	}

	// Change the actual visible party roster while the trailing 03 00 is
	// constant. A cache of only the last body would incorrectly swallow this.
	r195RewardParty(t, f.service, 19, 20)
	f.state.Members[1].GroupIndex = f.state.Members[0].GroupIndex
	changed := send("changed_party", session)
	r195LukeWantCompletionWire(t, changed, "kind1", "empty3", "state5")
	body, err := hex.DecodeString(changed[0].BodyHex)
	if err != nil || !r195WantOwners(parsePhase0CardOwners(t, body), 19, 20) {
		t.Fatal("changed party body was lost", err, changed)
	}
	r195LukeWantCompletionWire(t, send("changed_party_duplicate", session))

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(65*time.Second), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() {
		t.Fatal("party-only flow did not finish", errs)
	}
	flow.BreakUntil = time.Now().Add(5 * time.Minute)
	r195LukeWantCompletionWire(t, send("finished", session), "state5")
	r195LukeWantCompletionWire(t, send("finished_duplicate", session))
	flow.BreakUntil = flow.BreakUntil.Add(time.Minute)
	r195LukeWantCompletionWire(t, send("new_deadline", session), "state5")

	replacement := f.session()
	replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = session.channel, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	assets = lukeDisplayAssetSnapshot(t, f.service, replacement)
	r195LukeWantCompletionWire(t, send("reconnect_finished", replacement), "state4", "kind1", "empty3", "state5")
	r195LukeWantCompletionWire(t, send("reconnect_duplicate", replacement))
	if after := lukeDisplayAssetSnapshot(t, f.service, replacement); !reflect.DeepEqual(assets, after) {
		t.Fatal("reconnection projection changed committed assets")
	}
	r195LukeAssertNoSquadAssets(t, f, flow)

	// These are projection guard fixtures, not claims of real Anton/single or
	// phase1 progression. Reuse committed party data; do not invoke grants.
	for _, tc := range []struct {
		name   string
		mode   byte
		phase  byte
		single bool
	}{{"anton", 1, 0, false}, {"fiend", 3, 0, false}, {"luke_single", 2, 0, true}, {"luke_phase1", 2, 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			original := f.state.Battle
			b := cloneRuntimeRaidState(*f.state).Battle
			boundary := cloneCurrentAntonRewardFlow(flow)
			b.Mode, b.Phase, b.Single = tc.mode, tc.phase, tc.single
			boundary.Mode, boundary.Phase, boundary.Single = tc.mode, tc.phase, tc.single
			boundary.SquadShownAt, boundary.FinishedAt = time.Time{}, time.Time{}
			b.RewardFlow[tc.phase] = boundary
			f.state.Battle = b
			defer func() { f.state.Battle = original }()
			peer := f.session()
			peer.channel, peer.loginPacketSeen, peer.accountFromToken = replacement.channel, true, true
			f.service.bindGameSessionCharacter(peer, 19)
			// Establish only the projector's already-delivered-result precondition.
			peer.raidProjection.resultSent = true
			peer.raidProjection.resultIdentity = currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: 19, Generation: peer.characterGeneration}
			r195LukeWantCompletionWire(t, send("boundary_"+tc.name, peer), "state4", "kind1")
		})
	}
	if path := os.Getenv("R195_LUKE_PARTY_ONLY_WIRE_OUT"); path != "" {
		data, err := json.MarshalIndent(exported, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Separate actual-PVF leaf so the main thread can schedule heavy fixtures
// serially. Manual kind1 uses the shared single-body signature contract.
func TestR195LukePartyOnlyManualCompletionDoesNotReplayParty(t *testing.T) {
	f, session, flow := r195LukeCompletedPartyFixture(t)
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	r195LukeWantCompletionWire(t, r195LukeCompletionWire(t, f.conn.write.Bytes()), "kind1", "empty3")
	if card := flow.Party[20]; card != nil && card.Committed {
		t.Fatal("manual fixture unexpectedly committed the other recipient")
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	r195LukeWantCompletionWire(t, r195LukeCompletionWire(t, f.conn.write.Bytes()))
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	r195LukeWantCompletionWire(t, r195LukeCompletionWire(t, f.conn.write.Bytes()), "state5")
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("manual completion replayed", err)
	}
	r195LukeAssertNoSquadAssets(t, f, flow)
}
