package dnfbridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingEmptyInvitationReplayDoesNotLoadMutationPVFRules(t *testing.T) {
	s, session, _ := newChannelReconnectTest(t)
	s.wedding.rules = nil
	s.options.currentClientVersion = "official-24.9"
	s.bindGameSessionCharacter(session, 29)
	if err := s.sendInitialCurrentWeddingGuestLetters(session); err != nil {
		t.Fatal(err)
	}
	if s.wedding.rules != nil || s.wedding.loadErr != nil {
		t.Fatal("read-only invitation replay attempted to load mutation rules")
	}
}

func TestWeddingLoginBenefitsReplayFollowsFullActorInitialization(t *testing.T) {
	for _, phase := range []byte{wedding.Single, wedding.Engaged, wedding.Married} {
		t.Run(fmt.Sprintf("phase-%d", phase), func(t *testing.T) {
			f := weddingBenefitsAuditFixture(t, phase)
			t.Setenv(townDisplayOnlyEnv, "off")
			f.s.wedding.rules.Rings = make([]wedding.Ring, 4)
			f.s.wedding.rules.MaxLovePoint = 999
			f.s.wedding.guestRules = &wedding.GuestRules{Capacity: map[byte]int{0: 10}}
			f.s.wedding.extensionRules = &wedding.ExtensionRules{Capsule: wedding.CapsuleRules{ItemID: 10002910, MaximumOwn: 10, MaximumTaken: 10, Gain: 2, MaximumGauge: 10}}
			for _, cid := range []string{"29", "30"} {
				c, _, err := f.repos.Character.Load(context.Background(), cid)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.repos.Account.Save(context.Background(), r.AccountRecord{AccountID: c.AccountID}); err != nil {
					t.Fatal(err)
				}
				if phase == wedding.Married {
					c.Stats["wedding_v1_ring"] = 3
				}
				c.Stats["town_id"], c.Stats["area_id"] = 38, 1
				if err := r.SaveCharacterFields(context.Background(), f.repos.Character, c, r.CharacterFieldWedding, r.CharacterFieldLocation); err != nil {
					t.Fatal(err)
				}
			}
			for _, session := range []*gameSession{f.first, f.second} {
				if err := f.s.startGameSessionEvents(context.Background(), session); err != nil {
					t.Fatal(err)
				}
				defer stopTestGameSessionEvents(t, f.s, session)
				f.s.armCurrentInitialTownRoute(session, session.selectedCharacterID)
				session.initialTownRouteStage = currentInitialTownRouteActorBound
				if err := f.s.callGameSession(context.Background(), session, "login_actor_initialization", func() error {
					return f.s.sendCurrentInitialTownPlayerState(session, "login_benefits_test")
				}); err != nil {
					t.Fatal(err)
				}
				if err := f.s.callGameSession(context.Background(), session, "login_replay_barrier", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				lastMode1, lastInfo, lastRoom, infoCount := -1, -1, -1, 0
				var info []byte
				frames := weddingChurchLoadingFrames(t, session)
				for i, frame := range frames {
					if frame.op == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(frame.body) > 0 && frame.body[0] == 1 {
						lastMode1 = i
					}
					if frame.op == 1000 {
						lastInfo, info = i, frame.body
						infoCount++
					}
					if frame.op == 1008 {
						lastRoom = i
					}
				}
				if phase != wedding.Married {
					if lastMode1 < 0 || lastInfo < 0 || lastInfo >= lastMode1 || infoCount != 1 || info[14] != 255 {
						t.Fatal("non-married cold login changed its original clear/UI flow")
					}
					continue
				}
				if lastMode1 < 0 || lastInfo <= lastMode1 || lastRoom <= lastInfo || infoCount != 2 {
					t.Fatalf("character %d cold login leaves Info before actor reconstruction: mode1=%d Info=%d Room=%d", session.selectedCharacterID, lastMode1, lastInfo, lastRoom)
				}
				wantRing := byte(255)
				if phase == wedding.Married {
					wantRing = 3
				}
				if len(info) != 15 || info[4] != phase || info[14] != wantRing {
					t.Fatalf("unexpected final Info %x", info)
				}
				for i, frame := range frames {
					if i < lastInfo {
						continue
					}
					if frame.op == 1000 || frame.op == 1008 || frame.op == 1009 || frame.op == 1011 {
						raw, _ := json.Marshal(map[string]any{"character": session.selectedCharacterID, "phase": phase, "opcode": frame.op, "classification": frame.class, "payload_hex": hex.EncodeToString(frame.body)})
						t.Logf("LOGIN_BENEFIT_PACKET %s", raw)
					}
					if frame.op == 998 || frame.op == 1033 || frame.op == 1004 {
						t.Fatal("benefit replay must not open ceremony result")
					}
				}
			}
		})
	}
}
