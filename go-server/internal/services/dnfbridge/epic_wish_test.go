package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfepicwish "longheng.io/server/internal/modules/dnf/epicwish"
)

func TestNativeEpicWishSlotsAndBoundaries(t *testing.T) {
	slots := dnfepicwish.Slots{101000001, 0, 101000003, 0, 101000005}
	body := buildCurrentEpicWishInfoBody(slots)
	if len(body) != 21 || body[0] != 5 {
		t.Fatalf("body=%x", body)
	}
	// Independent native vector reads preserve empty positions and reset the
	// list; the effect membership check sees only the actual wished-for IDs.
	for i, id := range slots {
		got := int32(binary.LittleEndian.Uint32(body[1+4*i : 5+4*i]))
		if id == 0 {
			id = -1
		}
		if got != id {
			t.Fatalf("slot %d=%d want %d", i, got, id)
		}
	}
	decoded, err := decodeCurrentEpicWishUpdate(body)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != (dnfepicwish.Slots{101000001, -1, 101000003, -1, 101000005}) {
		t.Fatalf("slots=%v", decoded)
	}
	for _, bad := range [][]byte{nil, {6}, body[:20], append(append([]byte(nil), body...), 0), {1, 1, 2, 3}} {
		if _, err := decodeCurrentEpicWishUpdate(bad); err == nil {
			t.Fatalf("accepted %x", bad)
		}
	}
	if got, err := decodeCurrentEpicWishUpdate([]byte{0}); err != nil || got != (dnfepicwish.Slots{}) {
		t.Fatal("clear invalid")
	}
}

func TestEpicWishUpdateAndFreshSessionReplay(t *testing.T) {
	repos, _ := mustCurrentAvatarCompoundTestAssets(t, nil)
	source := currentAvatarCompoundTestPVF()
	source["equipment/avatar_hat_a.equ"] = "[equipment type] `[weapon]`\n[grade] 90\n[rarity] 4\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	service := currentAvatarCompoundTestService(repos, catalog)
	service.options.currentClientVersion = "official-24.9"
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	body := buildCurrentEpicWishInfoBody(dnfepicwish.Slots{41001, 0, 0, 0, 0})
	handled, err := service.handleCurrentEpicWishCommand(session, 1352, 1, body)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	packets := splitAllGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
	if len(packets) != 2 || packets[0].Header.MsgID != 1352 || len(packets[0].Body) != 1 || packets[0].Body[0] != 1 || packets[1].Header.MsgID != 1288 {
		t.Fatalf("packets=%+v", packets)
	}
	fresh := currentAvatarCompoundTestService(repos, catalog)
	fresh.options.currentClientVersion = "official-24.9"
	relog := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	if err := fresh.sendCurrentEpicWishSnapshot(relog, "relogin"); err != nil {
		t.Fatal(err)
	}
	p := splitAllGameServerUpperPackets(t, relog.conn.(*bufferConn).write.Bytes())
	if len(p) != 1 || string(p[0].Body) != string(body) {
		t.Fatalf("relogin=%+v", p)
	}
	before, _, _ := repos.Character.Load(context.Background(), "19")
	session.conn.(*bufferConn).write.Reset()
	bad := buildCurrentEpicWishInfoBody(dnfepicwish.Slots{41101}) // real epic avatar is not wishable equipment
	if _, err := service.handleCurrentEpicWishCommand(session, 1352, 1, bad); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Character.Load(context.Background(), "19")
	if dnfepicwish.Read(before) != dnfepicwish.Read(after) {
		t.Fatal("invalid update cleared saved selection")
	}
}
