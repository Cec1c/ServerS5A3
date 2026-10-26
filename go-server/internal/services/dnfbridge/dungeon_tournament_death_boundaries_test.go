package dnfbridge

import (
	"bytes"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Synthetic handler fixtures for duplicate and eliminated-tournament deaths.
// The sentinel-owner duplicate replays the cached authoritative death response;
// neither the room nor the tournament may advance again.
func tournamentBoundaryDeathSession(t *testing.T, runtime *runtimeDungeonState, connID string) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: connID, selectedCharacterID: 99}
	session.dungeon.runtime = runtime
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	return service, session, connection
}

// 已接受的死亡重复上报：不再推进、不改房间/场景，保持缓存通知重放。
// 并且重复上报之后状态机仍然活着（本轮的下一只照常推进）。
func TestTournamentDuplicateAcceptedDeathDoesNotAdvanceAgain(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	runtime, _ := tournamentStartMapBlockingFixture(t)
	service, session, connection := tournamentBoundaryDeathSession(t, runtime, "rest23-tournament-duplicate-death")

	firstKey := runtime.Room.Snapshot().Monsters[0].ObjectKey
	if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(firstKey)); err != nil {
		t.Fatalf("first op39 for round 1 actor %d: %v", firstKey, err)
	}
	if round := runtime.Tournament.currentRoundNumber(); round != 2 {
		t.Fatalf("round after the first accepted death=%d want 2", round)
	}
	roomAfterFirst := runtime.Room.Snapshot()
	sceneAfterFirst, _ := runtime.Session.Scene()
	if roomAfterFirst.Monsters[0].State != runtimeDungeonMonsterDefeated {
		t.Fatalf("round 1 actor state=%q want defeated", roomAfterFirst.Monsters[0].State)
	}
	written := connection.write.Len()

	// canAcceptActorDeath 对"已接受"的 sequence 返回 true（幂等准入），
	// 所以这条 op39 会穿过准入闸门；它必须在**不改房间/场景、不推进轮次**的前提下被吞掉。
	if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(firstKey)); err != nil {
		t.Fatalf("duplicate op39 for round 1 actor %d: %v", firstKey, err)
	}
	roomAfterDuplicate := runtime.Room.Snapshot()
	sceneAfterDuplicate, _ := runtime.Session.Scene()
	if round := runtime.Tournament.currentRoundNumber(); round != 2 {
		t.Fatalf("duplicate report advanced the round to %d (want 2)", round)
	}
	if !reflect.DeepEqual(roomAfterFirst, roomAfterDuplicate) {
		t.Fatalf("duplicate report mutated the room; before=%+v after=%+v", roomAfterFirst, roomAfterDuplicate)
	}
	if !reflect.DeepEqual(sceneAfterFirst, sceneAfterDuplicate) {
		t.Fatal("duplicate report mutated the scene")
	}
	// Sentinel requests reach the existing cached-death replay before the
	// normal already-defeated branch, even in a single-player fixture.
	replay, rest := splitGameServerUpperPacket(t, connection.write.Bytes()[written:])
	cached, ok := runtime.Room.DeathResponseBody(firstKey)
	if !ok || replay.Header.MsgID != uint16(dnfenum.CmdPacketNotifyDieMonster) || !bytes.Equal(replay.Body, cached) || len(rest) != 0 {
		t.Fatal("duplicate sentinel death did not replay exactly its cached authority")
	}

	// 状态机仍然活着：本轮（第 2 轮）的对手上报后照常推进到第 3 轮。
	secondKey := roomAfterDuplicate.Monsters[1].ObjectKey
	if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(secondKey)); err != nil {
		t.Fatalf("op39 for round 2 actor %d: %v", secondKey, err)
	}
	final := runtime.Room.Snapshot()
	if final.Monsters[1].State != runtimeDungeonMonsterDefeated {
		t.Fatalf("round 2 actor state=%q want defeated", final.Monsters[1].State)
	}
	if round := runtime.Tournament.currentRoundNumber(); round != 3 {
		t.Fatalf("round after the round 2 kill=%d want 3", round)
	}
	if final.Monsters[0].State != runtimeDungeonMonsterDefeated {
		t.Fatalf("round 1 actor state after later kills=%q want defeated", final.Monsters[0].State)
	}
	for _, index := range []int{2, 3} {
		if final.Monsters[index].State != runtimeDungeonMonsterAnnounced {
			t.Fatalf("future opponent %d state=%q want announced", index, final.Monsters[index].State)
		}
	}
}

// B. 赛制 terminal/eliminated 后，**尚活**对手（甚至就是本轮 actor）的 op39 必须被拒绝。
func TestTournamentTerminalEliminatedRejectsLivingOpponent(t *testing.T) {
	for _, killRoundOneFirst := range []bool{false, true} {
		name := "no-kill-yet"
		if killRoundOneFirst {
			name = "after-round-1-kill"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			runtime, _ := tournamentStartMapBlockingFixture(t)
			service, session, connection := tournamentBoundaryDeathSession(t, runtime, "rest23-tournament-terminal-eliminated")

			targetIndex := 0
			wantRound := byte(1)
			if killRoundOneFirst {
				firstKey := runtime.Room.Snapshot().Monsters[0].ObjectKey
				if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(firstKey)); err != nil {
					t.Fatalf("round 1 op39: %v", err)
				}
				if round := runtime.Tournament.currentRoundNumber(); round != 2 {
					t.Fatalf("round after the round 1 kill=%d want 2", round)
				}
				targetIndex = 1
				wantRound = 2
			}

			// Set the eliminated fixture through its state machine; this does not
			// exercise the player-death deadline handler.
			if completed, accepted := runtime.Tournament.tryEliminate(); !accepted {
				t.Fatalf("tryEliminate on an active tournament accepted=%t completed=%d", accepted, completed)
			}
			if !runtime.Tournament.isEliminated() {
				t.Fatal("terminal is not Eliminated after tryEliminate")
			}
			if round := runtime.Tournament.currentRoundNumber(); round != wantRound {
				t.Fatalf("round after elimination=%d want %d", round, wantRound)
			}

			before := runtime.Room.Snapshot()
			sceneBefore, _ := runtime.Session.Scene()
			// 目标必须是**尚活**对手，且就是本轮 actor（不是"未来轮"场景）。
			live := before.Monsters[targetIndex]
			if live.State != runtimeDungeonMonsterAnnounced {
				t.Fatalf("opponent %d state=%q want announced (必须是尚活对手)", targetIndex, live.State)
			}
			written := connection.write.Len()

			if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(live.ObjectKey)); err != nil {
				t.Fatalf("op39 for a living opponent after elimination: %v", err)
			}
			after := runtime.Room.Snapshot()
			sceneAfter, _ := runtime.Session.Scene()

			if after.Monsters[targetIndex].State != runtimeDungeonMonsterAnnounced {
				t.Fatalf("eliminated tournament accepted a living opponent death (state=%q)", after.Monsters[targetIndex].State)
			}
			if round := runtime.Tournament.currentRoundNumber(); round != wantRound {
				t.Fatalf("eliminated tournament advanced the round to %d (want %d)", round, wantRound)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("eliminated tournament mutated the room; before=%+v after=%+v", before, after)
			}
			if !reflect.DeepEqual(sceneBefore, sceneAfter) {
				t.Fatal("eliminated tournament mutated the scene")
			}
			if connection.write.Len() != written {
				t.Fatalf("eliminated tournament wrote %d bytes (want 0)", connection.write.Len()-written)
			}
			if runtime.tournamentReward != nil {
				t.Fatal("eliminated tournament created a reward state from a living opponent death")
			}
			// 重复上报同样必须被吞。
			if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(live.ObjectKey)); err != nil {
				t.Fatalf("duplicate op39 after elimination: %v", err)
			}
			if !reflect.DeepEqual(after, runtime.Room.Snapshot()) {
				t.Fatal("repeat op39 after elimination mutated the room")
			}
		})
	}
}
