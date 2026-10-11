package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dungeoncmd "longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

// This follows actual compiled hatchery topology using domain room movement
// and the authoritative room death API. It is not a physical client walk.
func reviewCompleteActualHatchery(t *testing.T, s *Service, session *gameSession, r *runtimeDungeonState) {
	t.Helper()
	for step := 0; step < 8; step++ {
		scene, ok := r.Session.Scene()
		if !ok {
			t.Fatal("missing scene")
		}
		room, _, err := newRuntimeDungeonRoom(scene, s.dungeonMonsterTable, firstDungeonMonsterObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		r.Room = room
		if _, err = room.AnnounceAllActors(r.Session); err != nil {
			t.Fatal(err)
		}
		var target uint32
		for _, m := range room.Snapshot().Monsters {
			target = m.ObjectKey
			if _, _, err = room.CommitActorDeathReport(m.ObjectKey, r.Session); err != nil {
				t.Fatal(err)
			}
		}
		for _, a := range room.Snapshot().ExtendedActors {
			if a.HostileReference == nil {
				continue
			}
			target = a.ObjectKey
			if _, _, err = room.CommitActorDeathReport(a.ObjectKey, r.Session); err != nil {
				t.Fatal(err)
			}
		}
		scene, _ = r.Session.Scene()
		if !scene.Cleared {
			t.Fatalf("actual room not clear: %s", scene.Coordinate.String())
		}
		if scene.Boss {
			if target == 0 {
				t.Fatal("actual boss room no actor")
			}
			if err = s.handleDungeonBossDieCheck(session, bossDieCheckRequestBody(session.selectedCharacterID, uint16(target))); err != nil {
				t.Fatal(err)
			}
			if err = s.handleDungeonSetPlayResult(session, make([]byte, currentDungeonPlayResultBaseSize)); err != nil {
				t.Fatal(err)
			}
			if err = s.handleDungeonCharacterStatistic(session, make([]byte, currentDungeonCharacterStatisticBodySize)); err != nil {
				t.Fatal(err)
			}
			if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.bossDieCheckAccepted || !r.settlementClearRewardSent {
				planErr := s.produceCurrentDungeonSettlementPlanLocked(context.Background(), session, r)
				t.Fatalf("actual boss did not complete/settle accepted=%v entry=%v reward=%v plan=%v phase=%v persisted=%v play=%v err=%v", r.bossDieCheckAccepted, r.settlementEntrySent, r.settlementClearRewardSent, r.settlementResultPlan != nil, r.settlementPhase, r.tutorialCompletionPersisted, r.settlementPlayResultReceived, planErr)
			}
			return
		}
		moved := false
		for _, c := range []worldmap.RoomCoordinate{{X: scene.Coordinate.X - 1, Y: scene.Coordinate.Y}, {X: scene.Coordinate.X + 1, Y: scene.Coordinate.Y}, {X: scene.Coordinate.X, Y: scene.Coordinate.Y - 1}, {X: scene.Coordinate.X, Y: scene.Coordinate.Y + 1}} {
			if c.X < 0 || c.Y < 0 || c.X > 255 || c.Y > 255 {
				continue
			}
			tr, e := r.Session.PreviewMoveByteTransition(byte(c.X), byte(c.Y))
			if e != nil || tr.Revisit {
				continue
			}
			if _, e = r.Session.MoveByte(byte(c.X), byte(c.Y)); e != nil {
				t.Fatal(e)
			}
			moved = true
			break
		}
		if !moved {
			t.Fatal("actual graph no forward room")
		}
	}
	t.Fatal("actual hatchery walk did not reach boss")
}

func TestReviewHatcheryRosterSingleMemberKeepsTownAndPartyGuards(t *testing.T) {
	for _, fault := range []string{"ready", "nonparty", "not_ready", "return_pending", "canonical_other_party", "member_absent"} {
		t.Run(fault, func(t *testing.T) {
			_, s, source, target, conn, peerConn := newTownCoPresenceTestService(t)
			state := alignedcmd.PartyState{PartyID: 12, UserID: 1001, Members: []alignedcmd.PartyMemberState{{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100}}}
			switch fault {
			case "nonparty":
				state.PartyID = 0
			case "member_absent":
				state.Members = []alignedcmd.PartyMemberState{{UserID: 1234}}
			case "not_ready":
				source.townSceneReadyCharacterID = 0
			case "return_pending":
				source.confirmedDungeonReturnStatePending = true
			case "canonical_other_party":
				fresh := &gameSession{conn: &bufferConn{}, channel: source.channel, accountID: source.accountID, selectedCharacterID: 1001}
				fresh.townSceneReadyCharacterID = 1001
				fresh.townPositionSnapshot = source.townPositionSnapshot
				storeRuntimePartyState(fresh, alignedcmd.PartyState{PartyID: 13, UserID: 1001, Members: state.Members})
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1001, AccountID: source.accountID, Session: fresh, TownID: 38, AreaID: 2})
				s.bindGameSessionCharacter(fresh, 1001)
				storeRuntimePartyState(fresh, alignedcmd.PartyState{PartyID: 13, UserID: 1001, Members: state.Members})
				if current, ok := s.onlineGameSession(1001); !ok || current != fresh {
					t.Fatal("canonical replacement fixture not committed")
				}
			}
			storeRuntimePartyState(source, state)
			_ = target
			conn.write.Reset()
			peerConn.write.Reset()
			if err := s.rebuildRuntimePartyTownCoPresenceAfterReturn(source, "review_single_party_return"); err != nil {
				t.Fatal(err)
			}
			if fault != "ready" {
				if conn.write.Len() != 0 || peerConn.write.Len() != 0 {
					t.Fatal("invalid party/town owner gained roster projection")
				}
				return
			}
			frames := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
			if len(frames) < 2 || frames[0].Header.Classification != 0 || frames[0].Header.MsgID != 153 {
				t.Fatal("one-member restoration did not begin with nonempty realtime roster")
			}
			if peerConn.write.Len() != 0 {
				t.Fatal("single party touched unrelated peer")
			}
		})
	}
}

func TestReviewActualHatcherySingleMemberPartyRestoresRosterAfterTownReturn(t *testing.T) {
	for _, mode := range []string{"default", "copresence_off"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "copresence_off" {
				t.Setenv(dungeonPartyCompletionReturnTownCopresenceEnv, "off")
			} else {
				t.Setenv(dungeonPartyCompletionReturnTownCopresenceEnv, "")
			}
			s, session, _, target := actualAntonHatchWarpOwnerFor(t, 222)
			repos, _ := s.repositoryGroup()
			character, _, err := repos.Character.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if character.Stats == nil {
				character.Stats = map[string]int64{}
			}
			character.Stats["exp"] = 0
			if err = repos.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			if err = repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{SyncedLevel: character.Level}}); err != nil {
				t.Fatal(err)
			}
			if _, found, err := repos.Inventory.Load(context.Background(), "99"); err != nil {
				t.Fatal(err)
			} else if !found {
				if err = repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
			}
			s.onlinePlayers = newOnlinePlayerManager()
			storeRuntimePartyState(session, alignedcmd.PartyState{PartyID: 12, UserID: 99, Members: []alignedcmd.PartyMemberState{{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100}}})
			if err = s.handleDungeonSelectUpper(session, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})); err != nil {
				t.Fatal(err)
			}
			r := session.dungeon.runtime
			reviewCompleteActualHatchery(t, s, session, r)
			session.conn.(*bufferConn).write.Reset()
			if err = s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != nil || !r.townReturnOp24Sent {
				t.Fatal("normal completed town return not committed")
			}
			if mode == "default" && !currentPartyTownReturnSceneReady(session) {
				t.Fatal("town presence boundary not ready; cannot judge roster")
			}
			var roster, realtime int
			for _, p := range splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes()) {
				if p.Header.Classification != 0 {
					continue
				}
				if p.Header.MsgID == 9 {
					roster++
				}
				if p.Header.MsgID == 153 {
					realtime++
				}
			}
			wantRealtime := 1
			if mode == "copresence_off" {
				wantRealtime = 0
			}
			if (mode == "default" && roster < 1) || realtime != wantRealtime {
				t.Fatalf("completed one-member party lost roster: op9=%d op153=%d", roster, realtime)
			}
		})
	}
}

func TestReviewActualHatcheryCompletionAndTownActionContract(t *testing.T) {
	for _, id := range []uint32{221, 222, 223, 224} {
		for _, action := range []byte{0, 1, 2} {
			t.Run(fmt.Sprintf("%d/action%d", id, action), func(t *testing.T) {
				s, session, _, target := actualAntonHatchWarpOwnerFor(t, id)
				repos, _ := s.repositoryGroup()
				character, _, err := repos.Character.Load(context.Background(), "99")
				if err != nil {
					t.Fatal(err)
				}
				if character.Stats == nil {
					character.Stats = map[string]int64{}
				}
				character.Stats["exp"] = 0
				if err = repos.Character.Save(context.Background(), character); err != nil {
					t.Fatal(err)
				}
				if err = repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{SyncedLevel: character.Level}}); err != nil {
					t.Fatal(err)
				}
				if _, found, err := repos.Inventory.Load(context.Background(), "99"); err != nil {
					t.Fatal(err)
				} else if !found {
					if err = repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.handleDungeonSelectUpper(session, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})); err != nil {
					t.Fatal(err)
				}
				r := session.dungeon.runtime
				if r == nil || r.Dungeon.ID != int64(id) {
					t.Fatal("entry prerequisite missing")
				}
				reviewCompleteActualHatchery(t, s, session, r)
				session.conn.(*bufferConn).write.Reset()
				if err := s.handleDungeonEplpCommand(session, []byte{1, action}); err != nil {
					t.Fatal(err)
				}
				switch action {
				case 0:
					if session.dungeon.runtime != r || r.settlementCardExitAckSent {
						t.Fatal("closed completed hatchery retry consumed return owner")
					}
					if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
						t.Fatal(err)
					}
					if session.dungeon.runtime != nil || !r.townReturnOp24Sent {
						t.Fatal("retry reject lost legal town route")
					}
				case 1:
					if session.dungeon.runtime != nil {
						t.Fatal("other selector did not retire completed hatchery")
					}
				case 2:
					if session.dungeon.runtime != nil || !r.townReturnOp24Sent {
						t.Fatal("hatchery did not return to original town")
					}
				}
				if r.settlementCardLayoutSent || r.settlementCardRewardCommitted {
					t.Fatal("hatchery fabricated ordinary card rewards")
				}
			})
		}
	}
}
