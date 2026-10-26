package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realSingleAntonHPFixture(t *testing.T, weekly int) (*raidStartGuardFixture, *gameSession) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual single Anton catalog candidate PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, a, 1)
	f.service.options.currentClientVersion = "official-24.9"
	f.state.CurrentLayout, f.state.CurrentUIFlags = true, [3]byte{1, 0, 0}
	f.state.Members[0].GroupIndex = 1
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	if weekly != 0 {
		policy, err := currentRaidWeeklyPolicy()
		if err != nil {
			t.Fatal(err)
		}
		epoch, err := raid.WeeklyEpoch(time.Now(), policy)
		if err != nil {
			t.Fatal(err)
		}
		character, _, err := f.repos.Character.Load(context.Background(), "19")
		if err != nil {
			t.Fatal(err)
		}
		character.Stats = map[string]int64{"raid_weekly_epoch_v1_1": epoch, "raid_weekly_clears_v1_1": int64(weekly)}
		if err := f.repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
	}
	rule, err := f.service.currentRaidAdmissionRule(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: rule.Count + 2}}}); err != nil {
		t.Fatal(err)
	}
	enterRaidStartWaitingFixture(t, f, "")
	state, ok, err := f.service.startRuntimeRaidWithEntryCost(session)
	if err != nil || !ok || state.Battle == nil || len(state.Battle.SingleHPSymbols) != 8 {
		t.Fatal("actual single admission", ok, err)
	}
	if got, _ := f.slotCount(t, "0:50"); got != 2 {
		t.Fatal("actual entry cost changed", got)
	}
	f.state.Battle.EnterAfter = time.Now().Add(-time.Second)
	f.state.Battle.Deadline = f.state.Battle.EnterAfter.Add(time.Duration(f.state.Battle.Configuration.PhaseLimitSeconds[0]) * time.Second)
	return f, session
}

func singleHPFrames(t *testing.T, raw []byte) ([][]byte, int) {
	t.Helper()
	var bodies [][]byte
	starts := 0
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		if packet.Header.MsgID == 583 {
			if packet.Header.Classification != 0 {
				t.Fatal("symbol opcode classification")
			}
			bodies = append(bodies, append([]byte(nil), packet.Body...))
		}
		if packet.Header.MsgID == 587 {
			starts++
		}
		raw = rest
	}
	return bodies, starts
}

func assertSingleHPFrame(t *testing.T, b *runtimeRaidBattle, body []byte) {
	t.Helper()
	if len(body) != 65 || body[0] != 8 {
		t.Fatal("native count/length", len(body))
	}
	cleared := map[uint32]bool{}
	for _, id := range b.SingleSequence[:b.SingleIndex] {
		cleared[id] = true
	}
	for i := uint32(0); i < 8; i++ {
		id := binary.LittleEndian.Uint32(body[1+8*i:])
		value := binary.LittleEndian.Uint32(body[5+8*i:])
		want := uint32(0)
		if cleared[5314+i] {
			want = 1
		}
		if id != 64+i || value != want {
			t.Fatal("real HP identity/value", id, value, want)
		}
	}
}

func TestRealPVFSingleAntonHPStartEveryOwnedClearAndReconnect(t *testing.T) {
	for weekly := 0; weekly < 2; weekly++ {
		t.Run(strconv.Itoa(weekly), func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, weekly)
			b := f.state.Battle
			if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			bodies, starts := singleHPFrames(t, f.conn.write.Bytes())
			if starts != 1 || len(bodies) != 2 || !bytes.Equal(bodies[0], []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}) {
				t.Fatal("initial reset/start sequence", starts, bodies)
			}
			assertSingleHPFrame(t, b, bodies[1])
			t.Logf("actual candidate PVF weekly=%d index=0 NOTI583 body=%x", weekly, bodies[1])
			f.conn.write.Reset()
			if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
				t.Fatal("initial state replay", err)
			}
			for index, id := range append([]uint32(nil), b.SingleSequence...) {
				captured := cloneRuntimeRaidState(*f.state)
				r := raidCompletedRuntimeFixture(t, session, id)
				if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
					t.Fatal(err)
				}
				at := b.StartedAt.Add(time.Duration(index+10) * time.Second)
				if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || !changed {
					t.Fatal("owned clear", index, changed, err)
				}
				if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || changed {
					t.Fatal("clear replay", changed, err)
				}
				f.conn.write.Reset()
				// A delayed callback selects the latest owner rather than its old prefix.
				if err := f.service.sendCurrentRaidProgressProjection(session, captured, false, false); err != nil {
					t.Fatal(err)
				}
				bodies, starts = singleHPFrames(t, f.conn.write.Bytes())
				if starts != 0 || len(bodies) != 1 {
					t.Fatal("clear replayed transition or omitted snapshot", starts, len(bodies))
				}
				assertSingleHPFrame(t, b, bodies[0])
				t.Logf("owned fixture weekly=%d index=%d NOTI583 body=%x", weekly, b.SingleIndex, bodies[0])
				f.conn.write.Reset()
				if err := f.service.sendCurrentRaidProgressProjection(session, captured, false, false); err != nil || f.conn.write.Len() != 0 {
					t.Fatal("version replay", err)
				}
				if err := f.service.sendCurrentRaidProgressProjection(session, captured, false, true); err != nil {
					t.Fatal(err)
				}
				bodies, starts = singleHPFrames(t, f.conn.write.Bytes())
				if starts != 0 || len(bodies) != 2 || !bytes.Equal(bodies[0], []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}) {
					t.Fatal("rejoin reset/snapshot", starts, len(bodies))
				}
				assertSingleHPFrame(t, b, bodies[1])
			}
			copy := cloneRuntimeRaidBattle(b)
			copy.SingleHPSymbols[5314] = 999
			if b.SingleHPSymbols[5314] != 64 {
				t.Fatal("frozen binding aliased clone")
			}
		})
	}
}

func TestRealPVFSingleAntonHPRejectsBadJournalBeforeAnyStartBytes(t *testing.T) {
	for _, name := range []string{"unmapped", "duplicate_symbol", "uncommitted_index", "journal_future", "journal_participant", "journal_attempt", "member_count"} {
		t.Run(name, func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, 0)
			b := f.state.Battle
			switch name {
			case "unmapped":
				delete(b.SingleHPSymbols, 5314)
			case "duplicate_symbol":
				b.SingleHPSymbols[5315] = b.SingleHPSymbols[5314]
			case "uncommitted_index":
				b.SingleIndex = 1
			case "member_count":
				b.MemberClears = map[uint16]uint32{19: 1}
			default:
				b.SingleIndex, b.MemberClears = 1, map[uint16]uint32{19: 1}
				run := currentRaidClearedRun{DungeonID: 5314, Phase: 0, Attempt: 1, Participants: []uint16{19}}
				if name == "journal_future" {
					run.DungeonID = 5315
				}
				if name == "journal_participant" {
					run.Participants[0] = 20
				}
				if name == "journal_attempt" {
					run.Attempt = 2
				}
				b.ClearedRuns = map[string]currentRaidClearedRun{"corrupt": run}
			}
			if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err == nil || f.conn.write.Len() != 0 || session.raidProjection.initialized {
				t.Fatal("invalid HP owner emitted/committed start", err)
			}
		})
	}
}

func TestRealPVFSingleAntonHPStaleAuthorityAndPartialWrite(t *testing.T) {
	for _, name := range []string{"account", "channel", "instance", "phase", "removed", "generation", "partial"} {
		t.Run(name, func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, 0)
			state := cloneRuntimeRaidState(*f.state)
			var partial *antonPartialWriteConn
			switch name {
			case "account":
				state.Battle.MemberAccounts[19] = "foreign"
			case "channel":
				session.channel.ID++
			case "instance":
				f.state.Battle.InstanceID = "replacement"
			case "phase":
				f.state.Battle.Phase = 1
			case "removed":
				f.state.Members = nil
			case "generation":
				replacement := f.session()
				replacement.conn = &bufferConn{}
				replacement.loginPacketSeen, replacement.accountFromToken = true, true
				f.service.bindGameSessionCharacter(replacement, 19)
				// Binding alone preserves the resident owner until the scene
				// transition promotes the replacement connection.
				f.service.promoteResidentGameSession(replacement, 19)
				if _, current := f.service.boundGameSessionCharacterSnapshot(replacement); !current || replacement.characterGeneration == session.characterGeneration {
					t.Fatal("replacement connection did not acquire a new authoritative generation")
				}
			case "partial":
				partial = &antonPartialWriteConn{Conn: session.conn}
				session.conn = partial
			}
			if err := f.service.sendCurrentRaidProgressProjection(session, state, true, false); err == nil || session.raidProjection.initialized {
				t.Fatal("invalid projection accepted", err)
			}
			if partial == nil {
				if f.conn.write.Len() != 0 {
					t.Fatal("stale authority emitted bytes")
				}
			} else {
				if !partial.closed || !session.raidProjection.failed {
					t.Fatal("partial frame socket reused")
				}
				calls := partial.calls
				if err := f.service.sendCurrentRaidProgressProjection(session, state, true, false); err == nil || calls != partial.calls {
					t.Fatal("failed partial snapshot replayed", err)
				}
			}
		})
	}
}
