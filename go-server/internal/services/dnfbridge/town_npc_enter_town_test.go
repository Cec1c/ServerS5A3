package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestCurrentTownNPCEnterTownShapeBindsPersistedSource(t *testing.T) {
	base := currentTownSetUserAreaRequest{TownID: 107, AreaID: 0, PositionX: 1151, PositionY: 259, Direction: 5, OpaqueU16A: 38, OpaqueU16B: 1, OpaqueU32: 20276}
	c := dnfrepo.CharacterRecord{Stats: map[string]int64{"town_id": 38, "area_id": 1}}
	for _, name := range []string{"valid", "wrong-source-town", "wrong-source-area", "zero-npc", "wrong-direction", "unknown-tail", "same-town"} {
		t.Run(name, func(t *testing.T) {
			r := base
			switch name {
			case "wrong-source-town":
				r.OpaqueU16A++
			case "wrong-source-area":
				r.OpaqueU16B++
			case "zero-npc":
				r.OpaqueU32 = 0
			case "wrong-direction":
				r.Direction = 4
			case "unknown-tail":
				r.OpaqueTailU8 = 1
			case "same-town":
				r.TownID = 38
			}
			if currentTownNPCEnterTownShape(r, c) != (name == "valid") {
				t.Fatalf("shape=%+v", r)
			}
		})
	}
}

func TestRealPVFHandleAbisEnterTownKeepsLevelAndQuestGates(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set real published PVF path")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"completed", "captured-runtime-coordinate", "active", "low-level", "wrong-npc", "wrong-source", "off"} {
		t.Run(name, func(t *testing.T) {
			service, session, repos := newTownMoveTest(t)
			service.options.currentClientVersion = "official-24.9"
			service.initialEquipmentArchive = archive
			service.townCatalog = table
			c, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			c.Level = 90
			if name == "low-level" {
				c.Level = 49
			}
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			status := "completed"
			if name == "active" {
				status = "active"
			}
			if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{8618: {Status: status}}}); err != nil {
				t.Fatal(err)
			}
			b := buildTownMoveRequest(107, 0, 1151, 259, 5)
			binary.LittleEndian.PutUint16(b[7:9], 38)
			binary.LittleEndian.PutUint16(b[9:11], 1)
			binary.LittleEndian.PutUint32(b[11:15], 20276)
			switch name {
			case "wrong-npc":
				binary.LittleEndian.PutUint32(b[11:15], 20277)
			case "captured-runtime-coordinate":
				// Actual RECV op36, 2026-10-06T21:12:02.803491946Z:
				// native NPC object has X916,Y194 and writer appends Y+30.
				binary.LittleEndian.PutUint16(b[2:4], 916)
				binary.LittleEndian.PutUint16(b[4:6], 224)
			case "wrong-source":
				binary.LittleEndian.PutUint16(b[9:11], 2)
			case "off":
				t.Setenv(townNPCEnterTownEnv, "off")
			}
			if err := service.handleTownSetUserArea(session, b); err != nil {
				t.Fatal(err)
			}
			got, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			data := session.conn.(*bufferConn).write.Bytes()
			if name == "completed" || name == "captured-runtime-coordinate" {
				if got.Stats["town_id"] != 107 || got.Stats["area_id"] != 0 {
					t.Fatalf("did not commit: %+v", got.Stats)
				}
				_, _, _ = splitTownAreaTransitionSequenceWithClearQuest(t, data, 29, 107, 0, 1151, 259, 5, 3)
			} else if len(data) != 0 || got.Stats["town_id"] != 38 || got.Stats["area_id"] != 1 {
				t.Fatalf("rejected route mutated location or wrote packets: %x %+v", data, got.Stats)
			}
			q, _, err := repos.Quest.Load(context.Background(), "29")
			if err != nil || q.States[8618].Status != status {
				t.Fatalf("quest changed: %+v %v", q, err)
			}
		})
	}
}
