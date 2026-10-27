package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// v151 02491396..024913BA resolves the slot's serial in the op105 map.
// On a miss it returns before setting level and tooltip controls 3600/3601;
// their actual PVF DefaultText is "null". Installing op105 later does not
// replay that UI notification. Exercise an empty client map, not a warm one.
func TestR177PetLoginTooltipHasMetadataAtItemNotification(t *testing.T) {
	for _, failAt := range []int{0, 1, 2, 3} {
		for _, fallback := range []bool{false, true} {
			name := "initial"
			if fallback {
				name = "fallback"
			}
			if failAt != 0 {
				name += fmt.Sprintf("-retry%d", failAt)
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
					"26": {SlotIndex: 26, ItemID: 400990167, RawEntry: testCurrentEquippedCreatureRaw(26, 400990167, 37)},
				}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
					"37": {PetKey: "37", CreatureKey: 37, ItemID: 400990167, Level: 7, Exp: 123, Satiety: 72, NameRaw: []byte("PetB")},
				}}); err != nil {
					t.Fatal(err)
				}
				conn := &creatorTutorialFailConn{failAt: failAt, err: errors.New("pet initialization interrupted")}
				service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				session := &gameSession{conn: conn, selectedCharacterID: 19}
				var err error
				if fallback {
					err = service.sendSelectedCreatureSceneReadyProjection(session, name)
				} else {
					err = service.sendSelectedCreatureInitialStateAfterMode1(session, name)
				}
				if failAt != 0 {
					if err == nil || session.selectedCreatureStateTableSent {
						t.Fatal("incomplete initialization suppressed its fallback retry")
					}
					conn.write.Reset()
					conn.failAt = 0
					err = service.sendSelectedCreatureSceneReadyProjection(session, "retry")
				}
				if err != nil {
					t.Fatal(err)
				}
				type event struct {
					Opcode uint16 `json:"opcode"`
					Body   []byte `json:"body"`
				}
				var events []event
				for stream := conn.write.Bytes(); len(stream) > 0; {
					p, next := splitCurrentGameServerUpperPacketAuto(t, stream)
					if binary.LittleEndian.Uint16(stream[1:3]) == 14 {
						// op14 uses the explicit 16-byte header even when its sequence
						// is nonzero; the legacy auto helper only recognizes seq=0.
						p, next = splitLongHengGameServerUpperPacket(t, stream)
					}
					body := p.Body
					if p.Header.MsgID != 14 {
						body = body[3:]
					}
					events = append(events, event{p.Header.MsgID, append([]byte(nil), body...)})
					stream = next
				}
				if dir := os.Getenv("DNF90_PET_TOOLTIP_FIXTURE_DIR"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(events)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0644); err != nil {
						t.Fatal(err)
					}
				}
				states := map[uint32][]byte{}
				notified := false
				for _, e := range events {
					switch e.Opcode {
					case 105:
						if len(e.Body) < 12 || e.Body[0] != 1 {
							t.Fatalf("bad fixture table %x", e.Body)
						}
						states[binary.LittleEndian.Uint32(e.Body[1:5])] = e.Body[5:12]
					case 14:
						key := binary.LittleEndian.Uint32(e.Body[9:13])
						state, found := states[key]
						if !found {
							t.Fatalf("cold-login tooltip stays null: item notification serial=%d preceded its op105 metadata", key)
						}
						if state[0] != 72 || binary.LittleEndian.Uint32(state[2:6]) != 123 || state[6] != 7 {
							t.Fatalf("wrong pet state at notification: %x", state)
						}
						notified = true
					}
				}
				if !notified {
					t.Fatal("no existing slot26 notification")
				}
				before := conn.write.Len()
				if err := service.sendSelectedCreatureSceneReadyProjection(session, "already_completed"); err != nil || conn.write.Len() != before {
					t.Fatal("completed projection repeated notifications", err)
				}
			})
		}
	}
}
