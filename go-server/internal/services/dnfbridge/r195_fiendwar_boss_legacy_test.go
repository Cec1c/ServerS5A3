package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"
	"time"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
)

func r195BossFrame(class byte, kind, value uint32) []byte {
	raw := make([]byte, proto.LegacyGameHeaderSize+8)
	raw[0] = class
	binary.LittleEndian.PutUint16(raw[1:3], currentFiendWarBossCommand)
	binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
	binary.LittleEndian.PutUint16(raw[11:13], 1)
	binary.LittleEndian.PutUint32(raw[proto.LegacyGameHeaderSize:], kind)
	binary.LittleEndian.PutUint32(raw[proto.LegacyGameHeaderSize+4:], value)
	return raw
}

func r195BossLiveFixture(t *testing.T) (*raidStartGuardFixture, []*gameSession, []*runtimeDungeonState, []*deadlineAuditV10Conn) {
	t.Helper()
	f, peers := actualCurrentFiendWar(t, 2)
	f.state.Started = true
	f.state.Battle.EnterAfter = time.Now().Add(-time.Minute)
	f.state.Battle.Deadline = time.Now().Add(time.Hour)
	b := f.state.Battle
	for _, name := range []string{"top command center state", "mid command center state", "bot command center state"} {
		b.FiendWar.Symbols[b.FiendWar.Config.Symbols[name]] = 0
	}
	if err := b.FiendWar.Apply(fiendwar.FiendWarEvent{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	syncCurrentFiendWarDungeons(b)
	table, err := town.Load(context.Background(), f.service.initialEquipmentArchive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.service.townCatalog = table
	runs := make([]*runtimeDungeonState, len(peers))
	for i, peer := range peers {
		area := 30 + i
		managed, ok := f.service.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(*f.state, f.state.Members[i:i+1]))
		if !ok {
			t.Fatal("cannot create solo encounter party")
		}
		_, run := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
		run.Dungeon.ID = int64(b.FiendWar.Config.Areas[area].Dungeon)
		run.AccountID = peer.accountID
		run.Character = repo.CharacterRecord{CharacterID: strconv.Itoa(int(peer.selectedCharacterID)), AccountID: peer.accountID}
		run.partyInstance = &dungeonPartyInstance{key: dungeonPartyRunKey{PartyID: uint16(managed.PartyID)}}
		peer.dungeon.runtime = run
		runs[i] = run
		b.FiendWar.MemberAreas[peer.selectedCharacterID] = area
		setCurrentFiendWarTestOrigin(peer, area)
		peer.townSceneReadyCharacterID = peer.selectedCharacterID
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(peer, run, nil); err != nil {
			t.Fatal(err)
		}
	}
	b.FiendWar.PickClone = func([]int) int { return 0 }
	conns := make([]*deadlineAuditV10Conn, len(peers))
	for i, peer := range peers {
		conns[i] = &deadlineAuditV10Conn{}
		peer.conn = conns[i]
		peer.postStartMapPlayerStateSent = true
		f.service.commitCurrentFiendWarReservation(runs[i])
	}
	for _, mapID := range []int{306000463, 306000460} {
		if err := b.FiendWar.Apply(fiendwar.FiendWarEvent{Kind: "on enter map", ID: mapID}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if b.FiendWar.Value("boss pattern state") != 1111 || b.FiendWar.Value("top boss state") != 5 || b.FiendWar.Value("bot boss state") != 6 {
		t.Fatal("real PVF failed to initialize a shared four-ball pool")
	}
	return f, peers, runs, conns
}

// The production 00:32 request was cmd=1/type1528/body_len=8. Exercise that
// envelope, including both recipients' actual 1491 packets, not BossReport alone.
func TestR195FiendWarBossLegacySharedThreePlusOne(t *testing.T) {
	f, peers, _, conns := r195BossLiveFixture(t)
	for step, tc := range []struct{ peer, value, pool, dark, white int }{
		{0, 5, 111, 7, 6},
		{0, 7, 101, 8, 6},
		{0, 8, 100, 4, 6},
		{1, 6, 0, 9, 9},
	} {
		if err := f.service.handleLegacyGamePacket(peers[tc.peer], r195BossFrame(1, 1, uint32(tc.value))); err != nil {
			t.Fatal(err)
		}
		b := f.state.Battle.FiendWar
		if b.Value("boss pattern state") != tc.pool || b.Value("top boss state") != tc.dark || b.Value("bot boss state") != tc.white {
			t.Fatalf("step %d: pool/dark/white=%d/%d/%d, want %d/%d/%d", step, b.Value("boss pattern state"), b.Value("top boss state"), b.Value("bot boss state"), tc.pool, tc.dark, tc.white)
		}
		r160WaitRaidProjection(t, f, peers)
		for i, conn := range conns {
			found := false
			values := map[uint32]uint32{}
			wire := conn.bytes()
			for len(wire) > 0 {
				packet, rest := splitGameServerUpperPacket(t, wire)
				wire = rest
				if packet.Header.MsgID != currentFiendWarSymbol || packet.Header.Classification != 0 {
					continue
				}
				body := packet.Body
				if len(body) == 0 || len(body) != 1+8*int(body[0]) {
					t.Fatalf("invalid symbol projection %x", body)
				}
				for off := 1; off < len(body); off += 8 {
					values[binary.LittleEndian.Uint32(body[off:])] = binary.LittleEndian.Uint32(body[off+4:])
					if binary.LittleEndian.Uint32(body[off:]) == b.Config.Symbols["boss pattern state"] && binary.LittleEndian.Uint32(body[off+4:]) == uint32(tc.pool) {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("step %d peer %d did not receive shared remaining pool %d", step, i, tc.pool)
			}
			if values[b.Config.Symbols["top boss state"]] != uint32(tc.dark) || values[b.Config.Symbols["bot boss state"]] != uint32(tc.white) {
				t.Fatalf("step %d peer %d did not receive both boss states: %v", step, i, values)
			}
		}
		// Replayed completion cannot remove a second ball or advance either side.
		before := f.state.Battle.FiendWar.Clone()
		version := f.state.Battle.ProgressVersion
		if err := f.service.handleLegacyGamePacket(peers[tc.peer], r195BossFrame(1, 1, uint32(tc.value))); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Symbols, f.state.Battle.FiendWar.Symbols) || version != f.state.Battle.ProgressVersion {
			t.Fatal("replayed mechanism changed shared progress")
		}
	}
	if f.state.Battle.PhaseCompleted || f.state.Battle.FiendWar.Complete {
		t.Fatal("four mechanisms alone granted a boss clear")
	}
}

func TestR195FiendWarBossBothTransportsHPAndWindow(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			f, peers, _, _ := r195BossLiveFixture(t)
			send := func(peer int, class byte, kind, value uint32) {
				t.Helper()
				raw := r195BossFrame(class, kind, value)
				var err error
				if transport == "legacy" {
					err = f.service.handleLegacyGamePacket(peers[peer], raw)
				} else {
					var frame []byte
					frame, err = proto.BuildChannelPacket(currentFiendWarBossCommand, raw[proto.LegacyGameHeaderSize:], 1, class)
					if err == nil {
						// BuildChannelPacket treats zero as its default command
						// class. This control deliberately sends a notification.
						frame[0] = class
						err = f.service.handleGameUpper(peers[peer], frame)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			send(0, 0, 1, 5)
			if f.state.Battle.FiendWar.Value("boss pattern state") != 1111 {
				t.Fatal("notification class advanced mechanism")
			}
			send(0, 1, 2, 80)
			if f.state.Battle.FiendWar.BossWindowDone[0] {
				t.Fatal("premature window accepted")
			}
			send(0, 1, 0, 75)
			if f.state.Battle.FiendWar.Value("top boss view hp") != 75 || f.state.Battle.FiendWar.Value("boss pattern state") != 1111 {
				t.Fatal("HP report lost or changed pool")
			}
			r160WaitRaidProjection(t, f, peers)
			for _, step := range []struct {
				side  int
				state uint32
			}{{0, 5}, {0, 7}, {0, 8}, {1, 6}} {
				send(step.side, 1, 1, step.state)
				r160WaitRaidProjection(t, f, peers)
			}
			if f.state.Battle.FiendWar.Value("boss pattern state") != 0 {
				t.Fatal("mechanisms did not finish")
			}
			send(0, 1, 2, 20)
			r160WaitRaidProjection(t, f, peers)
			if f.state.Battle.FiendWar.Value("boss pattern state") != 0 {
				t.Fatal("one side alone restarted pool")
			}
			send(1, 1, 2, 60)
			r160WaitRaidProjection(t, f, peers)
			b := f.state.Battle.FiendWar
			if b.Value("boss pattern state") != 1111 || b.Value("top boss hp") != 40 || b.Value("bot boss hp") != 40 || b.Value("top boss state") == b.Value("bot boss state") || b.BossWindowDone != [2]bool{} {
				t.Fatal("failed window did not balance and restart shared round")
			}
			if f.state.Battle.PhaseCompleted || b.Complete {
				t.Fatal("window report granted clear")
			}
		})
	}
}

func TestR195FiendWarBossLegacyRejectsInvalidOwnerAndFrame(t *testing.T) {
	f, peers, runs, _ := r195BossLiveFixture(t)
	for _, name := range []string{"wrong_class", "truncated", "extra_bytes", "wrong_ball", "unknown_kind", "unauthenticated", "foreign_account", "old_instance", "old_run", "departed", "returning", "not_placed", "other_channel"} {
		t.Run(name, func(t *testing.T) {
			peer, run, b := peers[0], runs[0], f.state.Battle
			frame := r195BossFrame(1, 1, 5)
			before := b.FiendWar.Clone()
			version := b.ProgressVersion
			switch name {
			case "wrong_class":
				frame[0] = 0
			case "truncated":
				frame = frame[:len(frame)-1]
			case "extra_bytes":
				frame = append(frame, 0)
			case "wrong_ball":
				frame = r195BossFrame(1, 1, 6)
			case "unknown_kind":
				frame = r195BossFrame(1, 3, 5)
			case "unauthenticated":
				peer.accountFromToken = false
				defer func() { peer.accountFromToken = true }()
			case "foreign_account":
				old := peer.accountID
				peer.accountID = "foreign"
				defer func() { peer.accountID = old }()
			case "old_instance":
				old := run.raidEntry.InstanceID
				run.raidEntry.InstanceID = "obsolete"
				defer func() { run.raidEntry.InstanceID = old }()
			case "old_run":
				old := run.raidEntry.RunID
				run.raidEntry.RunID = "obsolete"
				defer func() { run.raidEntry.RunID = old }()
			case "departed":
				b.RunDepartures = map[string]map[uint16]bool{run.raidEntry.RunID: {peer.selectedCharacterID: true}}
				defer func() { b.RunDepartures = nil }()
			case "returning":
				run.townReturnPending = true
				defer func() { run.townReturnPending = false }()
			case "not_placed":
				peer.postStartMapPlayerStateSent = false
				defer func() { peer.postStartMapPlayerStateSent = true }()
			case "other_channel":
				old := peer.channel.ID
				peer.channel.ID++
				defer func() { peer.channel.ID = old }()
			}
			binary.LittleEndian.PutUint32(frame[3:7], uint32(len(frame)))
			if err := f.service.handleLegacyGamePacket(peer, frame); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Symbols, b.FiendWar.Symbols) || version != b.ProgressVersion {
				t.Fatal("invalid request mutated shared boss state")
			}
		})
	}
}

// Waiting must be projected to both clients, and the final ball must release
// both sides in the same state snapshot. Repeat with either side finishing first.
func TestR195FiendWarBossSharedWindowBarrierAndNextRound(t *testing.T) {
	for _, fast := range []int{0, 1} {
		t.Run(strconv.Itoa(fast), func(t *testing.T) {
			f, peers, _, conns := r195BossLiveFixture(t)
			names := [2]string{"top boss state", "bot boss state"}
			slow := 1 - fast
			send := func(side int, kind, value uint32) {
				t.Helper()
				if err := f.service.handleLegacyGamePacket(peers[side], r195BossFrame(1, kind, value)); err != nil {
					t.Fatal(err)
				}
				r160WaitRaidProjection(t, f, peers)
			}
			assertBothClients := func() {
				t.Helper()
				b := f.state.Battle.FiendWar
				for i, conn := range conns {
					values := map[uint32]uint32{}
					for wire := conn.bytes(); len(wire) > 0; {
						packet, rest := splitGameServerUpperPacket(t, wire)
						wire = rest
						if packet.Header.MsgID != currentFiendWarSymbol || packet.Header.Classification != 0 {
							continue
						}
						body := packet.Body
						if len(body) == 0 || len(body) != 1+8*int(body[0]) {
							t.Fatalf("invalid symbol projection %x", body)
						}
						for off := 1; off < len(body); off += 8 {
							values[binary.LittleEndian.Uint32(body[off:])] = binary.LittleEndian.Uint32(body[off+4:])
						}
					}
					for _, name := range []string{"top boss state", "bot boss state", "boss pattern state"} {
						got, exists := values[b.Config.Symbols[name]]
						if !exists || got != uint32(b.Value(name)) {
							t.Fatalf("client %d %s=%d, exists=%t; want %d", i, name, got, exists, b.Value(name))
						}
					}
				}
			}
			for round := 0; round < 2; round++ {
				lastBall := f.state.Battle.FiendWar.Value(names[slow])
				for n := 0; n < 3; n++ {
					send(fast, 1, uint32(f.state.Battle.FiendWar.Value(names[fast])))
				}
				b := f.state.Battle.FiendWar
				if b.Value("boss pattern state") == 0 || b.Value(names[fast]) != 4 || b.Value(names[slow]) != lastBall {
					t.Fatal("one client entered vulnerability before the shared pool was empty")
				}
				assertBothClients()
				version, before := f.state.Battle.ProgressVersion, b.Clone()
				send(fast, 2, 0)
				if f.state.Battle.ProgressVersion != version || !reflect.DeepEqual(before.Symbols, f.state.Battle.FiendWar.Symbols) {
					t.Fatal("premature native window tail committed")
				}
				send(slow, 1, uint32(lastBall))
				b = f.state.Battle.FiendWar
				if b.Value("boss pattern state") != 0 || b.Value(names[0]) != 9 || b.Value(names[1]) != 9 {
					t.Fatal("last ball did not release both sides")
				}
				assertBothClients()
				send(fast, 2, 60)
				if f.state.Battle.FiendWar.Value("boss pattern state") != 0 {
					t.Fatal("first tail alone reset the pool")
				}
				send(slow, 2, 80)
				b = f.state.Battle.FiendWar
				if b.Value("boss pattern state") != 1111 || b.Value(names[0]) == b.Value(names[1]) || b.BossWindowDone != [2]bool{} {
					t.Fatal("failed shared window did not restart four balls")
				}
				assertBothClients()
				if f.state.Battle.PhaseCompleted || b.Complete {
					t.Fatal("failed window granted clear")
				}
			}
		})
	}
}
