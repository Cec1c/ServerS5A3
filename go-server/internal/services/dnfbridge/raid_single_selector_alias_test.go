package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFSingleAntonSelectorFirstMapProjection(t *testing.T) {
	for weekly := 0; weekly < 2; weekly++ {
		f, session := realSingleAntonHPFixture(t, weekly)
		if err := f.service.sendCurrentRaidBattleState(session, *f.state); err != nil {
			t.Fatal(err)
		}
		raw := f.conn.write.Bytes()
		frames := 0
		for len(raw) > 0 {
			packet, rest := splitGameServerUpperPacket(t, raw)
			raw = rest
			if packet.Header.MsgID != 585 {
				continue
			}
			frames++
			states := map[uint32]byte{}
			for i := 0; i < int(packet.Body[1]); i++ {
				off := 2 + i*5
				states[binary.LittleEndian.Uint32(packet.Body[off:off+4])] = packet.Body[off+4]
			}
			if got, ok := states[210]; !ok || got != 0 {
				t.Fatalf("live native-only selector has no first UI slot: weekly=%d alias210=(%d,%t) states=%v", weekly, got, ok, states)
			}
			if states[5314] != 0 {
				t.Fatal("native first map identity lost")
			}
		}
		if frames != 2 {
			t.Fatalf("start/progress585frames=%d", frames)
		}
	}
}

func singleSelectorStates(t *testing.T, raw []byte) map[uint32]byte {
	t.Helper()
	var states map[uint32]byte
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		if packet.Header.MsgID != 585 {
			continue
		}
		states = map[uint32]byte{}
		if len(packet.Body) != 2+int(packet.Body[1])*5+4 {
			t.Fatal("585 boundary")
		}
		for i := 0; i < int(packet.Body[1]); i++ {
			off := 2 + i*5
			states[binary.LittleEndian.Uint32(packet.Body[off:])] = packet.Body[off+4]
		}
	}
	if states == nil {
		t.Fatal("missing585")
	}
	return states
}
func TestRealPVFSingleAntonSelectorProgressAndNativeOwner(t *testing.T) {
	for weekly := 0; weekly < 2; weekly++ {
		t.Run(strconv.Itoa(weekly), func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, weekly)
			b := f.state.Battle
			for index := 0; index < 3; index++ {
				f.conn.write.Reset()
				if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, true); err != nil {
					t.Fatal(err)
				}
				states := singleSelectorStates(t, f.conn.write.Bytes())
				id := b.SingleSequence[index]
				alias, _ := currentSingleAntonSelectorAlias(id)
				open := 0
				for ui := uint32(210); ui <= 224; ui++ {
					if state, ok := states[ui]; ok && state == 0 {
						open++
						if ui != alias {
							t.Fatal("other weekly/stale alias open", ui, index)
						}
					}
				}
				if open != 1 || states[alias] != 0 || states[id] != 0 {
					t.Fatal("current native/alias gate", states)
				}
				for _, native := range b.SingleSequence {
					if states[native] != b.Dungeons[indexOfSingleNative(b, native)].State {
						t.Fatal("native state changed")
					}
				}
				native := f.service.resolveCurrentSingleAntonSelector(session, alias)
				if native != id || !f.service.currentRaidDungeonSelectionAllowed(session, native) {
					t.Fatal("alias entry not native owner", native, id)
				}
				for future := index + 1; future < len(b.SingleSequence); future++ {
					a, _ := currentSingleAntonSelectorAlias(b.SingleSequence[future])
					if states[a] != 2 || f.service.currentRaidDungeonSelectionAllowed(session, f.service.resolveCurrentSingleAntonSelector(session, a)) {
						t.Fatal("future alias admitted", a)
					}
				}
				hp, _ := singleHPFrames(t, f.conn.write.Bytes())
				assertSingleHPFrame(t, b, hp[len(hp)-1])
				r := raidCompletedRuntimeFixture(t, session, id)
				if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
					t.Fatal(err)
				}
				if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, b.StartedAt.Add(time.Duration(index+10)*time.Second)); err != nil || !changed {
					t.Fatal("native clear", changed, err)
				}
				if b.SingleIndex != index+1 {
					t.Fatal("native clear index")
				}
				for _, run := range b.ClearedRuns {
					if run.DungeonID < 5314 {
						t.Fatal("alias leaked to accounting", run.DungeonID)
					}
				}
			}
		})
	}
}
func indexOfSingleNative(b *runtimeRaidBattle, id uint32) int {
	for i, n := range b.SingleSequence {
		if n == id {
			return i
		}
	}
	return -1
}
func TestRealPVFSingleAntonSelectorGuards(t *testing.T) {
	for _, name := range []string{"unstarted", "unpaid_no_battle", "wrong_account", "nonmember", "wrong_channel", "ordinary_channel", "generation", "team", "luke", "older_client", "future", "expired"} {
		t.Run(name, func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, 0)
			switch name {
			case "unstarted":
				f.state.Started = false
			case "unpaid_no_battle":
				f.state.Battle = nil
			case "wrong_account":
				session.accountID = "other"
			case "nonmember":
				delete(f.state.Battle.MemberAccounts, session.selectedCharacterID)
			case "wrong_channel":
				session.channel.ID++
			case "ordinary_channel":
				session.channel.Type = 1
			case "generation":
				replacement := f.session()
				replacement.loginPacketSeen = true
				replacement.accountFromToken = true
				f.service.bindGameSessionCharacter(replacement, 19)
			case "team":
				f.state.Battle.Single = false
			case "luke":
				f.state.Battle.Mode = 2
			case "older_client":
				f.service.options.currentClientVersion = "legacy"
			case "future":
			case "expired":
				f.state.Battle.Deadline = time.Now().Add(-time.Second)
			}
			requested := uint32(210)
			if name == "future" {
				requested, _ = currentSingleAntonSelectorAlias(f.state.Battle.SingleSequence[1])
			}
			resolved := f.service.resolveCurrentSingleAntonSelector(session, requested)
			if name == "future" || name == "expired" {
				if f.service.currentRaidDungeonSelectionAllowed(session, resolved) {
					t.Fatal("closed alias allowed")
				}
			} else if resolved != requested {
				t.Fatal("alias escaped ownership", resolved)
			}
			if got := f.service.resolveCurrentSingleAntonSelector(session, 9999); got != 9999 {
				t.Fatal("unknown rewritten")
			}
			switch name {
			case "unstarted", "unpaid_no_battle", "wrong_account", "nonmember", "wrong_channel", "generation", "future", "expired":
				body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
				binary.LittleEndian.PutUint32(body, requested)
				f.conn.write.Reset()
				if err := f.service.handleDungeonSelectUpper(session, body); err != nil {
					t.Fatal(err)
				}
				if session.dungeon.runtime != nil {
					t.Fatal("rejected alias committed runtime")
				}
				packet, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
				if packet.Header.MsgID != 16 || len(packet.Body) != 3 || packet.Body[0] != 0 || len(rest) != 0 {
					t.Fatal("rejected alias not failure ACK", packet.Header, packet.Body, len(rest))
				}
			}
		})
	}
}
func TestRealPVFSingleAntonSelectorActualOp16Entry(t *testing.T) {
	for weekly := 0; weekly < 2; weekly++ {
		t.Run(strconv.Itoa(weekly), func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, weekly)
			s := f.service
			a := s.initialEquipmentArchive
			table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
			if err != nil {
				t.Fatal(err)
			}
			resolver, err := worldmap.NewResolver(table)
			if err != nil {
				t.Fatal(err)
			}
			monsters, err := newPVFDungeonMonsterCatalog(a)
			if err != nil {
				t.Fatal(err)
			}
			ai, err := newPVFDungeonAICharacterCatalog(a)
			if err != nil {
				t.Fatal(err)
			}
			s.worldMapTable = table
			s.worldMapResolver = resolver
			s.dungeonMonsterTable = monsters
			s.dungeonAICharacterTable = ai
			s.dungeonChoice = func(int) (int, error) { return 0, nil }
			s.dungeonSeed = func() (uint32, error) { return 1, nil }
			character, _, err := f.repos.Character.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			character.Level = 90
			if character.Stats == nil {
				character.Stats = map[string]int64{}
			}
			character.Stats["fatigue"] = 156
			character.Stats["town_id"] = 38
			character.Stats["area_id"] = 1
			character.Stats["pos_x"] = 450
			character.Stats["pos_y"] = 234
			character.Stats["direction"] = 5
			character.Stats["area_state"] = 3
			if err := f.repos.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			for step := 0; step < 2; step++ {
				bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
				body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
				id := f.state.Battle.SingleSequence[step]
				alias, _ := currentSingleAntonSelectorAlias(id)
				binary.LittleEndian.PutUint32(body, alias)
				binary.LittleEndian.PutUint16(body[9:], 0xffff)
				f.conn.write.Reset()
				if err := s.handleDungeonSelectUpper(session, body); err != nil {
					t.Fatal(err)
				}
				if session.dungeon.runtime == nil {
					t.Fatal("actual op16 did not commit native runtime", f.logText(t))
				}
				r := session.dungeon.runtime
				if r.Request.DungeonID != id || r.Dungeon.ID != int64(id) || r.raidEntry == nil {
					t.Fatal("actual entry alias leaked", r.Request, r.Dungeon.ID, r.raidEntry)
				}
				if binary.LittleEndian.Uint32(body) != alias {
					t.Fatal("input body mutated")
				}
				var start bool
				raw := f.conn.write.Bytes()
				for len(raw) > 0 {
					p, rest := splitGameServerUpperPacket(t, raw)
					raw = rest
					if p.Header.MsgID == currentDungeonStartNotification {
						start = true
					}
				}
				if !start {
					t.Fatal("actual native map not started")
				}
				if step == 0 {
					// Combat completion is the existing owned-clear fixture; both op16 entries
					// below execute the real PVF loading, freeze, commit and start-map path.
					completed := raidCompletedRuntimeFixture(t, session, id)
					if err := s.freezeCurrentRaidDungeonGroupEntry(session, completed, nil); err != nil {
						t.Fatal(err)
					}
					if _, changed, err := s.reduceCurrentRaidDungeonClearLocked(session, completed, f.state.Battle.StartedAt.Add(10*time.Second)); err != nil || !changed {
						t.Fatal("first native clear", changed, err)
					}
					session.dungeon.runtime = nil // test boundary after completed return; no return-handler claim.
					f.conn.write.Reset()
					if err := s.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, true); err != nil {
						t.Fatal(err)
					}
					states := singleSelectorStates(t, f.conn.write.Bytes())
					next, _ := currentSingleAntonSelectorAlias(f.state.Battle.SingleSequence[1])
					if states[alias] != 2 || states[next] != 0 {
						t.Fatal("next UI/native gate", states)
					}
				}

			}

		})
	}
}

func TestRealPVFSingleAntonSelectorMalformedMappingFailsBeforeSend(t *testing.T) {
	for _, name := range []string{"unknown", "row_identity", "native_gate", "duplicate_alias"} {
		t.Run(name, func(t *testing.T) {
			f, session := realSingleAntonHPFixture(t, 0)
			b := f.state.Battle
			switch name {
			case "unknown":
				b.SingleSequence[0] = 9999
				b.Dungeons[0].ID = 9999
				b.SingleHPSymbols[9999] = 64
				delete(b.SingleHPSymbols, 5314)
			case "row_identity":
				b.Dungeons[0].ID = 5315
			case "native_gate":
				b.Dungeons[1].State = 0
			case "duplicate_alias":
				b.SingleSequence[1] = 5321
				b.Dungeons[1].ID = 5321
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err == nil || f.conn.write.Len() != 0 {
				t.Fatal("malformed mapping sent start", err, f.conn.write.Len())
			}
		})
	}
}
