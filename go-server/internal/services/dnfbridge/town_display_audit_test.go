package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestTownDisplayAuditAuxiliaryPresenceUsesCanonicalDisplay(t *testing.T) {
	for _, role := range []string{"actor", "observer"} {
		t.Run(role, func(t *testing.T) {
			t.Setenv(townDisplayOnlyEnv, "on")
			_, s, observer, actorSession, observerConn, _ := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = "official-24.9"
			primary := actorSession
			if role == "observer" {
				primary = observer
			}
			aux := &gameSession{conn: &bufferConn{}, connID: "display-audit-aux", channel: primary.channel,
				residentChannel: primary.residentChannel, selectedCharacterID: primary.selectedCharacterID,
				townActorOwnerChannel:           primary.townActorOwnerChannel,
				connectionTownActorOwnerChannel: primary.connectionTownActorOwnerChannel}
			s.bindGameSessionCharacter(aux, aux.selectedCharacterID)
			s.registerLiveGameSession(aux)
			t.Cleanup(func() { s.unregisterLiveGameSession(aux) })
			if _, current := s.boundGameSessionCharacterSnapshot(aux); current {
				t.Fatal("fixture replaced canonical session")
			}
			row, _ := s.onlinePlayers.PlayerForCharacter(primary.selectedCharacterID)
			row.Session = aux
			s.onlinePlayers.EnterArea(&row)
			actor, _ := s.onlinePlayers.PlayerForCharacter(actorSession.selectedCharacterID)
			if err := s.sendTownRemoteActorState(observer, actor, "town_copresence_actor_state_newcomer_to_existing"); err != nil {
				t.Fatal(err)
			}
			frames := townDisplayFrames(t, observerConn.write.Bytes())
			requireTownPartyActorHPFrameIDs(t, frames, 2, 9)
			observer.townDisplayMu.Lock()
			marked := observer.townDisplayPeers[actorSession.selectedCharacterID]
			observer.townDisplayMu.Unlock()
			if marked.session != actorSession || !s.boundGameSessionCharacterCurrent(marked) {
				t.Fatal("display projection retained an auxiliary identity")
			}
			observerConn.write.Reset()
			state, result, joined := s.createOrJoinManagedRuntimeParty(actorSession, observer)
			if !joined || !result.OK {
				t.Fatalf("join: %+v", result)
			}
			if err := s.sendRuntimePartyRosterLocal(observer, state, "auxiliary_promotion"); err != nil {
				t.Fatal(err)
			}
			requireTownDisplayAuditPromotionBeforeHUD(t, townDisplayFrames(t, observerConn.write.Bytes()))
		})
	}
}

func TestTownDisplayAuditRosterRefreshPromotesBeforeRealtime(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	_, s, observer, member, observerConn, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	actor, _ := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if err := s.sendTownRemoteActorState(observer, actor, "town_copresence_actor_state_existing_to_newcomer"); err != nil {
		t.Fatal(err)
	}
	observerConn.write.Reset()
	state, result, joined := s.createOrJoinManagedRuntimeParty(member, observer)
	if !joined || !result.OK {
		t.Fatalf("join failed: %+v", result)
	}
	if err := s.sendRuntimePartyRosterLocal(observer, state, "town_display_audit_roster"); err != nil {
		t.Fatal(err)
	}
	requireTownDisplayAuditPromotionBeforeHUD(t, townDisplayFrames(t, observerConn.write.Bytes()))
	observerConn.write.Reset()
	if err := s.sendRuntimePartyRosterLocal(observer, state, "repeat_roster"); err != nil {
		t.Fatal(err)
	}
	frames := townDisplayFrames(t, observerConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 153, 9)
}

func requireTownDisplayAuditPromotionBeforeHUD(t *testing.T, frames []upperFrame) {
	t.Helper()
	mode1, realtime, roster, fullCount := -1, -1, -1, 0
	for index, frame := range frames {
		if frame.msgID == 2 && len(frame.body) > 0 && frame.body[0] == 1 {
			mode1 = index
			fullCount++
		}
		if frame.msgID == 153 && realtime < 0 {
			realtime = index
		}
		if frame.msgID == 9 && roster < 0 {
			roster = index
		}
		if frame.msgID == 11 {
			t.Fatal("roster refresh replayed endpoint handshake")
		}
	}
	if fullCount != 1 || mode1 < 0 || realtime < 0 || mode1 >= realtime || roster <= realtime {
		t.Fatalf("full actor then realtime then roster required: fullCount=%d mode1=%d realtime=%d roster=%d", fullCount, mode1, realtime, roster)
	}
}

func TestTownDisplayAuditSoloTransitionsStayLight(t *testing.T) {
	for _, transition := range []string{"member_leave", "leader_leave", "kick", "two_singletons"} {
		t.Run(transition, func(t *testing.T) {
			t.Setenv(townDisplayOnlyEnv, "on")
			_, s, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = "official-24.9"
			startTownPeerTest(t, s, leader, nil)
			startTownPeerTest(t, s, member, nil)
			if transition == "two_singletons" {
				for _, session := range []*gameSession{leader, member} {
					identity, _ := s.boundGameSessionCharacterSnapshot(session)
					row, ok := s.runtimePartyMemberForIdentity(identity)
					if !ok {
						t.Fatal("runtime member unavailable")
					}
					result := s.runtimePartyManagerForService().EnsureLeader(row, alignedcmd.PartyState{MaxMembers: 4})
					if !result.OK {
						t.Fatal(result.Reason)
					}
					s.publishRuntimePartySnapshot(result.Party)
				}
			} else {
				if ok, _, reason, _ := s.forceManagedRuntimeParty(1001, 1002); !ok {
					t.Fatal(reason)
				}
				sender, typ, body := member, uint16(13), []byte(nil)
				if transition == "leader_leave" {
					sender = leader
				}
				if transition == "kick" {
					sender, typ, body = leader, 14, []byte{1}
				}
				if handled, err := s.handleOnlinePartyCommand(sender, typ, body); !handled || err != nil {
					t.Fatalf("handled=%t error=%v", handled, err)
				}
			}
			drainTownPeerTest(t, s, leader)
			drainTownPeerTest(t, s, member)
			leaderConn.write.Reset()
			memberConn.write.Reset()
			actor, _ := s.onlinePlayers.PlayerForCharacter(1002)
			peer, _ := s.onlinePlayers.PlayerForCharacter(1001)
			s.broadcastTownPlayerEnter(&actor, []onlinePlayerInfo{peer})
			drainTownPeerTest(t, s, leader)
			drainTownPeerTest(t, s, member)
			for _, conn := range []*bufferConn{leaderConn, memberConn} {
				requireTownPartyActorHPFrameIDs(t, townDisplayFrames(t, conn.write.Bytes()), 2, 9, 23)
			}
		})
	}
}

func TestTownDisplayAuditFinishLoadingReplayStaysLightAndIdempotent(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	t.Setenv(currentTownCopresenceResendEnv, "on")
	_, s, observer, _, observerConn, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	s.resendTownCopresencePeerActorsAfterFinishLoading(observer, "display_audit_finish_loading")
	requireTownPartyActorHPFrameIDs(t, townDisplayFrames(t, observerConn.write.Bytes()), 2, 9, 23)
	observerConn.write.Reset()
	s.resendTownCopresencePeerActorsAfterFinishLoading(observer, "display_audit_finish_loading_repeat")
	if observerConn.write.Len() != 0 {
		t.Fatal("same generation replayed actors twice")
	}
}

func TestTownDisplayAuditRetiredPeerCannotPromote(t *testing.T) {
	for _, retire := range []string{"new_character_generation", "left_area"} {
		t.Run(retire, func(t *testing.T) {
			t.Setenv(townDisplayOnlyEnv, "on")
			_, s, observer, member, observerConn, _ := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = "official-24.9"
			actor, _ := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
			if err := s.sendTownRemoteActorState(observer, actor, "town_copresence_actor_state_existing_to_newcomer"); err != nil {
				t.Fatal(err)
			}
			observerConn.write.Reset()
			state, result, joined := s.createOrJoinManagedRuntimeParty(member, observer)
			if !joined || !result.OK {
				t.Fatal(result.Reason)
			}
			if retire == "left_area" {
				s.onlinePlayers.LeaveArea(member.selectedCharacterID)
			} else {
				s.mu.Lock()
				member.characterGeneration++
				s.mu.Unlock()
			}
			if err := s.upgradeTownDisplayPeersForParty(observer, state); err != nil {
				t.Fatal(err)
			}
			if observerConn.write.Len() != 0 {
				t.Fatal("retired display actor was rebuilt")
			}
			observer.townDisplayMu.Lock()
			remaining := len(observer.townDisplayPeers)
			observer.townDisplayMu.Unlock()
			if remaining != 0 {
				t.Fatal("retired display identity retained")
			}
		})
	}
}

func TestTownDisplayAuditCanonicalChannelAndSessionAdmission(t *testing.T) {
	for _, change := range []string{"actor_channel", "observer_channel", "actor_session", "observer_session"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv(townDisplayOnlyEnv, "on")
			_, s, observer, member, _, _ := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = "official-24.9"
			actor, _ := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
			switch change {
			case "actor_channel":
				member.channel.ID++
			case "observer_channel":
				observer.channel.ID++
			case "actor_session":
				actor.Session = &gameSession{selectedCharacterID: member.selectedCharacterID, channel: member.channel}
			case "observer_session":
				observer = &gameSession{selectedCharacterID: observer.selectedCharacterID, channel: observer.channel}
			}
			if s.currentTownDisplayOnlyProjection(observer, actor, "town_copresence_actor_state_newcomer_to_existing") {
				t.Fatal("noncanonical session or changed channel admitted")
			}
		})
	}
}

func TestTownDisplayAuditFullRosterRefreshKeepsWire(t *testing.T) {
	_, s, observer, member, observerConn, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	state, result, joined := s.createOrJoinManagedRuntimeParty(member, observer)
	if !joined || !result.OK {
		t.Fatal(result.Reason)
	}
	var baseline []upperFrame
	for _, setting := range []string{"off", "on"} {
		t.Setenv(townDisplayOnlyEnv, setting)
		observerConn.write.Reset()
		if err := s.sendRuntimePartyRosterLocal(observer, state, "full_actor_roster_refresh"); err != nil {
			t.Fatal(err)
		}
		frames := townDisplayFrames(t, observerConn.write.Bytes())
		requireTownPartyActorHPFrameIDs(t, frames, 153, 9)
		if setting == "off" {
			// The parser owns each body; buffer Reset may overwrite its storage.
			for _, frame := range frames {
				baseline = append(baseline, upperFrame{msgID: frame.msgID, body: append([]byte(nil), frame.body...)})
			}
		} else {
			// Header sequence and its checksum advance on every send. Compare
			// the ordered message IDs and exact payloads, not those counters.
			for index, frame := range frames {
				if frame.msgID != baseline[index].msgID || !bytes.Equal(frame.body, baseline[index].body) {
					t.Fatalf("complete actor roster payload changed at frame %d", index)
				}
			}
		}
	}
}
