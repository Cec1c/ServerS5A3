package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFDungeonWearAppearanceKeepsCommittedDungeonOwner(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current369d PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ownerContextUseClientChannelEnv, "on")
	r := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "9", AccountID: "account-1", Name: "Actor9", Job: "14", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "9", Entries: map[string]dnfrepo.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, repositoryProvider: func() (dnfrepo.Group, bool) { return r, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, accountID: "account-1", selectedCharacterID: 9, channel: channelcatalog.Channel{ID: 23, Type: 23}, connectionTownActorOwnerChannel: 23, townActorOwnerChannel: 23}
	// Use the production entry gate and the committed pre-op27 actor lifecycle.
	resetDungeonEntrySceneGates(session)
	if err := s.sendCurrentPreDungeonContextPlayerState(session, "actual_entry"); err != nil {
		t.Fatal(err)
	}
	for _, refresh := range []struct {
		name string
		call func(*gameSession, string, string) error
	}{{"mode0", s.sendSelectedActorMode0AppearanceRefresh}, {"mode1", s.sendSelectedActorMode1AppearanceRefresh}, {"mode0_mode1", s.sendSelectedActorAppearanceRefresh}} {
		conn.write.Reset()
		if err := refresh.call(session, "wear_after_ack", refresh.name); err != nil {
			t.Fatal(err)
		}
		raw := conn.write.Bytes()
		actors := 0
		for len(raw) > 0 {
			p, rest := splitLongHengGameServerUpperPacket(t, raw)
			raw = rest
			if p.Header.MsgID == 2 {
				actors++
				if len(p.Body) < 5 || p.Body[4] != 0 {
					t.Fatalf("%s rebuilt actor with town owner=%d while dungeon owner0 committed", refresh.name, p.Body[4])
				}
			}
		}
		if actors == 0 {
			t.Fatalf("%s did not send actor", refresh.name)
		}
	}
	resetDungeonReturnSceneGates(session)
	session.townActorOwnerChannel = 23
	conn.write.Reset()
	if err := s.sendSelectedActorMode0AppearanceRefresh(session, "town_return", "restore"); err != nil {
		t.Fatal(err)
	}
	p, _ := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if p.Body[4] != 23 {
		t.Fatalf("return owner=%d want23", p.Body[4])
	}
}
