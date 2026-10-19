package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type ordinaryRidableFormalFixture struct {
	peer   *peerUserInfoActiveDungeonFixture
	scene  worldmap.DungeonRoomScene
	before worldmap.Dungeon
}

func newOrdinaryRidableFormalFixture(t *testing.T) *ordinaryRidableFormalFixture {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual a56 PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, found := table.FindDungeon(164)
	if !found {
		t.Fatal("actual ordinary dungeon 164 missing")
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, 164, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	f := newPeerUserInfoActiveDungeonFixture(t)
	f.service.initialEquipmentArchive = archive
	f.service.dungeonMonsterTable = monsters
	f.service.dungeonChoice = func(int) (int, error) { return 0, nil }
	owner, err := f.service.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, nil)
	if err != nil {
		t.Fatal(err)
	}
	var scene worldmap.DungeonRoomScene
	for index, session := range []*gameSession{f.source, f.target} {
		selected := owner
		if index == 1 {
			selected, err = f.service.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, owner)
			if err != nil {
				t.Fatal(err)
			}
		}
		r, current := ordinaryRidableEntryRuntime(t, f.service, dungeon, topology, worldmap.RoomCoordinate{X: 2, Y: 1}, selected)
		r.Character, found, err = f.repositories.Character.Load(context.Background(), strconv.Itoa(int(session.selectedCharacterID)))
		if err != nil || !found {
			t.Fatal("fixture character", err)
		}
		r.AccountID = f.service.accountIDForSession(session)
		r.lifecycleToken = 7
		r.ridableReportChannelID = session.channel.ID
		r.ridableReportChannelType = session.channel.Type
		session.dungeon.runToken = 7
		session.dungeon.runtime = r
		session.loginPacketSeen = true
		session.accountFromToken = true
		session.currentFinishLoadingStateSent = true
		session.currentFinishLoadingCompletionSent = true
		if f.service.adoptPartyDungeonInstance(session, r, current) == nil {
			t.Fatal("fixture party instance missing")
		}
		scene = current
	}
	return &ordinaryRidableFormalFixture{peer: f, scene: scene, before: dungeon}
}

func ordinaryRidableReportBody(state byte, key uint32, x, y byte) []byte {
	body := make([]byte, 7)
	body[0] = state
	binary.LittleEndian.PutUint32(body[1:5], key)
	body[5] = x
	body[6] = y
	return body
}

func sendOrdinaryRidableFormal(t *testing.T, f *ordinaryRidableFormalFixture, session *gameSession, sequence uint16, class byte, body []byte) {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(316, body, sequence, class)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.peer.service.handleGameUpper(session, frame); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*gameSession{f.peer.source, f.peer.target} {
		if s.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("lifecycle report generated an unproven ACK/notification")
		}
	}
}

func ordinaryRidableFullProjection(t *testing.T, session *gameSession, scene worldmap.DungeonRoomScene, mode byte) []currentDungeonStartMapRidableEntry {
	t.Helper()
	r := session.dungeon.runtime
	body, err := buildCurrentDungeonMoveStartMapBody(r, currentDungeonMovePlan{Target: scene, TargetRoom: r.Room, NextObjectKey: r.NextObjectKey, Seed: r.Seed, PayloadMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	if body[13] != mode {
		t.Fatal("full projection changed mode", body[13], mode)
	}
	// The independent existing row reader checks Build specifically; normalize
	// only the already-asserted mode byte when using it to read Refresh rows.
	copyBody := append([]byte(nil), body...)
	copyBody[13] = currentDungeonStartMapPayloadBuild
	return ordinaryRidableRows(t, copyBody)
}

func TestRealPVFOrdinaryRidableFormalDestroyFiltersSharedFullRefresh(t *testing.T) {
	f := newOrdinaryRidableFormalFixture(t)
	if f.peer.source.dungeon.runtime.randomizedObjects == f.peer.target.dungeon.runtime.randomizedObjects {
		t.Fatal("frozen owner itself should remain cloned")
	}
	for _, s := range []*gameSession{f.peer.source, f.peer.target} {
		if len(ordinaryRidableFullProjection(t, s, f.scene, currentDungeonStartMapPayloadRefresh)) != 4 {
			t.Fatal("initial ordinary vehicle count")
		}
	}
	beforeCharacter, _, err := f.peer.repositories.Character.Load(context.Background(), "1002")
	if err != nil {
		t.Fatal(err)
	}
	// The non-host's mount/release reports must not retire or create a vehicle.
	for i, state := range []byte{2, 2, 1, 1} {
		sendOrdinaryRidableFormal(t, f, f.peer.target, uint16(i+10), 0, ordinaryRidableReportBody(state, 69000, 2, 1))
	}
	if len(ordinaryRidableFullProjection(t, f.peer.source, f.scene, currentDungeonStartMapPayloadRefresh)) != 4 {
		t.Fatal("mount/release changed spawn population")
	}
	sendOrdinaryRidableFormal(t, f, f.peer.target, 20, 0, ordinaryRidableReportBody(3, 69000, 2, 1))
	for _, s := range []*gameSession{f.peer.source, f.peer.target} {
		for _, mode := range []byte{currentDungeonStartMapPayloadBuild, currentDungeonStartMapPayloadRefresh} {
			rows := ordinaryRidableFullProjection(t, s, f.scene, mode)
			if len(rows) != 3 {
				t.Fatalf("destroyed ordinary vehicle was reprojected: mode=%d rows=%+v", mode, rows)
			}
			for _, row := range rows {
				if row.ObjectCode == 69000 {
					t.Fatal("retired ordinary vehicle returned")
				}
			}
		}
		if err := s.dungeon.runtime.randomizedObjects.validate(f.before, 0); err != nil {
			t.Fatal("retirement changed frozen source", err)
		}
	}
	// Duplicate sequence/body and new-sequence terminal reports stay terminal;
	// a later local mount or release does not infer a replacement generation.
	for i, state := range []byte{3, 3, 2, 1} {
		sendOrdinaryRidableFormal(t, f, f.peer.target, uint16(20+i), 0, ordinaryRidableReportBody(state, 69000, 2, 1))
	}
	if len(ordinaryRidableFullProjection(t, f.peer.source, f.scene, currentDungeonStartMapPayloadRefresh)) != 3 {
		t.Fatal("later report revived terminal source")
	}
	r := f.peer.source.dungeon.runtime
	visit := &runtimeDungeonRoomVisit{Scene: f.scene, Room: r.Room, Seed: r.Seed}
	cached, err := buildCurrentDungeonMoveStartMapBody(r, currentDungeonMovePlan{Target: f.scene, TargetRoom: r.Room, TargetVisit: visit, Seed: r.Seed, PayloadMode: currentDungeonStartMapPayloadCached})
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 16 || cached[14] != 0 {
		t.Fatalf("cached projection changed: %x", cached)
	}
	// Host changes keep the run-scoped terminal owner. No 296 is sent.
	change := f.peer.service.runtimePartyManagerForService().TransferLeader(1001, f.peer.service.characterGenerationForTest(f.peer.source), 1002, f.peer.service.characterGenerationForTest(f.peer.target))
	if !change.OK {
		t.Fatalf("change fixture host: %+v", change)
	}
	f.peer.service.publishRuntimePartySnapshot(change.Party)
	if len(ordinaryRidableFullProjection(t, f.peer.target, f.scene, currentDungeonStartMapPayloadRefresh)) != 3 {
		t.Fatal("host transfer lost terminal state")
	}
	afterCharacter, _, err := f.peer.repositories.Character.Load(context.Background(), "1002")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) {
		t.Fatal("asset/character mutation during lifecycle notification")
	}
}

func TestRealPVFOrdinaryRidableFormalRejectsInvalidContext(t *testing.T) {
	for _, name := range []string{"short", "tail", "state", "high_key", "ff", "wrong_room", "wrong_class", "not_ready", "old_binding", "account", "run_token", "channel", "old_sequence", "namespace"} {
		t.Run(name, func(t *testing.T) {
			f := newOrdinaryRidableFormalFixture(t)
			s := f.peer.target
			r := s.dungeon.runtime
			body := ordinaryRidableReportBody(3, 69000, 2, 1)
			seq := uint16(30)
			class := byte(0)
			switch name {
			case "short":
				body = body[:6]
			case "tail":
				body = append(body, 0)
			case "state":
				body[0] = 4
			case "high_key":
				binary.LittleEndian.PutUint32(body[1:5], 0x10010d88)
			case "ff":
				body[5], body[6] = 255, 255
			case "wrong_room":
				body[5] = 3
			case "wrong_class":
				class = 2
			case "not_ready":
				s.currentFinishLoadingCompletionSent = false
			case "old_binding":
				f.peer.service.bindGameSessionCharacter(&gameSession{accountID: "dnf:2"}, 1002)
			case "account":
				s.accountID = "dnf:1"
			case "run_token":
				s.dungeon.runToken++
			case "channel":
				s.channel.ID++
			case "old_sequence":
				s.guild.purchaseSequence.observe(31)
			case "namespace":
				r.randomizedObjects.Entries[0].SpawnMode = 1
			}
			sendOrdinaryRidableFormal(t, f, s, seq, class, body)
			// Use the unmodified peer as the independent frozen projection.
			if len(ordinaryRidableFullProjection(t, f.peer.source, f.scene, currentDungeonStartMapPayloadRefresh)) != 4 {
				t.Fatal("rejected request retired a shared source")
			}
		})
	}
}
