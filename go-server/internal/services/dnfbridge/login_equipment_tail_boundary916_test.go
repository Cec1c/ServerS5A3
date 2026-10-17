package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestLoginTailBoundary916(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "unowned_route", true: "owned_route"}[active], func(t *testing.T) {
			repos := testRepositoryGroup()
			seedLoginTailReassertEquipment(t, repos, loginTailReassertP38Entries(t))
			conn := &bufferConn{}
			service := newLoginEquipmentTestService(repos)
			session := newLoginEquipmentTestSession(conn, 119)
			if active {
				session.initialTownRouteCharacterID = 119
				session.initialTownRouteStage = currentInitialTownRouteTransitionSent
			}
			if err := service.replayLoginEquipmentSceneReadyContainer(session, "regression916"); err != nil {
				t.Fatal(err)
			}
			stream := conn.write.Bytes()
			hadContainer := false
			updates := 0
			for len(stream) > 0 {
				p, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
				stream = rest
				if p.Header.Classification != 0 {
					continue
				}
				if p.Header.MsgID == 13 && len(p.Body) > 0 && p.Body[0] == 3 {
					hadContainer = true
				}
				if p.Header.MsgID != 14 || len(p.Body) < 3 || p.Body[0] != 3 {
					continue
				}
				if !hadContainer {
					t.Fatal("tail update preceded login container")
				}
				updates++
				if binary.LittleEndian.Uint16(p.Body[1:3]) != 2 {
					t.Fatal("wrong update count")
				}
				for _, row := range decodeCurrentItemListTestRows(t, p.Body[3:]) {
					slot := binary.LittleEndian.Uint16(row)
					seed := binary.LittleEndian.Uint32(row[6:10])
					if slot == 12 {
						if seed != 346360515 || row[0x57] != 4 || row[10] != 9 {
							t.Fatalf("main identity %x", row)
						}
					} else if slot == 24 {
						if seed != 999999998 || row[0x57] != 2 || row[10] != 7 {
							t.Fatalf("sub identity %x", row)
						}
					} else {
						t.Fatalf("unexpected slot %d", slot)
					}
				}
			}
			if active && updates != 1 {
				t.Fatalf("cold-login boundary delivered %d tail updates, want one", updates)
			}
			if !active && (updates != 0 || hadContainer) {
				t.Fatal("unowned route received equipment")
			}
		})
	}
}
