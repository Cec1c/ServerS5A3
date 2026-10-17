package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFExpertJobStoreRejectsUnavailableAndNPCPositions(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("requires matching real PVF")
	}
	source, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobs, err := expertjob.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(ctx, source, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	area, ok := towns.FindArea(38, 0)
	if !ok {
		t.Fatal("real town 38/0 missing")
	}
	m, ok := currentTownAreaMapDocument(source, area)
	if !ok {
		t.Fatal("real map missing")
	}
	t.Logf("real map=%s movable=%v NPCs=%+v", area.MapPath, m.VirtualMovableArea, m.NPCs)
	type point struct {
		name    string
		x, y    int16
		failure byte
	}
	points := []point{{"outside-map", -30000, -30000, 19}}
	for _, npc := range m.NPCs {
		for i := 0; i+3 < len(m.VirtualMovableArea); i += 4 {
			r := m.VirtualMovableArea[i : i+4]
			x, y := max(r[0], min(npc.Position.X, r[0]+r[2])), max(r[1], min(npc.Position.Y, r[1]+r[3]))
			if x > npc.Position.X-80 && x < npc.Position.X+80 && y > npc.Position.Y-150 && y < npc.Position.Y+150 {
				points = append(points, point{"npc-nearby", int16(x), int16(y), 82})
				break
			}
		}
		if len(points) == 2 {
			break
		}
	}
	if len(points) != 2 {
		t.Fatal("no real NPC inside movable area")
	}
	// Select an actual walkable point outside every declared NPC rectangle.
	for i := 0; i+3 < len(m.VirtualMovableArea) && len(points) == 2; i += 4 {
		r := m.VirtualMovableArea[i : i+4]
		for x := r[0]; x <= r[0]+r[2] && len(points) == 2; x += 20 {
			for y := r[1]; y <= r[1]+r[3]; y += 20 {
				blocked := false
				for _, npc := range m.NPCs {
					if x > npc.Position.X-80 && x < npc.Position.X+80 && y > npc.Position.Y-150 && y < npc.Position.Y+150 {
						blocked = true
					}
				}
				if !blocked {
					points = append(points, point{"legal-position", int16(x), int16(y), 0})
					break
				}
			}
		}
	}
	if len(points) != 3 {
		t.Fatal("no legal real placement point")
	}
	for _, kind := range []byte{currentExpertJobDisjointStoreKind, currentExpertJobEnchantStoreKind} {
		for _, p := range points {
			t.Run(fmt.Sprintf("kind%d/%s", kind, p.name), func(t *testing.T) {
				repos := memory.NewMemoryGroup()
				job := int64(expertjob.DisjointerType)
				if kind == currentExpertJobEnchantStoreKind {
					job = int64(expertjob.EnchanterType)
				}
				before := dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 1000, "expert_job_type": job, "expert_job_exp": 10, currentExpertJobMachineGradeStat: 1, currentExpertJobMachineEnduranceStat: 300}}
				if err := repos.Character.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				s := currentBoosterTestService(items, repos)
				s.expertJobCatalog = jobs
				s.townCatalog = towns
				s.onlinePlayers = newOnlinePlayerManager()
				ownerConn, peerConn := &bufferConn{}, &bufferConn{}
				session := &gameSession{conn: ownerConn, selectedCharacterID: 19}
				peer := &gameSession{conn: peerConn, selectedCharacterID: 20}
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, TownID: 38, AreaID: 0, Session: session})
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 20, TownID: 38, AreaID: 0, Session: peer})
				body := make([]byte, 16)
				body[0] = kind
				binary.LittleEndian.PutUint32(body[1:5], 1)
				body[5] = 'x'
				binary.LittleEndian.PutUint32(body[6:10], 1)
				binary.LittleEndian.PutUint16(body[10:12], uint16(p.x))
				binary.LittleEndian.PutUint16(body[12:14], uint16(p.y))
				binary.LittleEndian.PutUint16(body[14:16], 65535)
				if err := s.handleCurrentExpertJobStoreCreate(session, 598, body); err != nil {
					t.Fatal(err)
				}
				packets := drainUpperPackets(t, ownerConn)
				var ack []byte
				for _, packet := range packets {
					if packet.msgID == 598 {
						ack = packet.body
					}
				}
				want := []byte{1}
				if p.failure != 0 {
					want = []byte{0, p.failure}
				}
				if !bytes.Equal(ack, want) {
					t.Fatalf("placement (%d,%d) ACK=%x want=%x", p.x, p.y, ack, want)
				}
				if p.failure != 0 {
					if len(s.expertJobStores) != 0 || peerConn.write.Len() != 0 {
						t.Fatal("rejected placement registered or broadcast store")
					}
				} else if len(s.expertJobStores) != 1 {
					t.Fatal("legal placement did not register")
				}
				after, _, err := repos.Character.Load(ctx, "19")
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("placement changed durable character: %+v %v", after, err)
				}
			})
		}
	}
}
