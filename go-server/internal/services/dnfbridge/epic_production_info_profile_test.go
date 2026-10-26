package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Exercise the actual notice sender, including profile choice, ownership,
// catalog lookup and serialization. The old vendor fixture remains on 1355;
// fixed v103 registers the same 32-byte epic state layout on class0/1354.
func TestEpicProductionInfoNoticeProfileCompatibility(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []struct {
		name string
		want uint16
	}{
		{"", 1355}, {"90cn", 1355}, {"legacy", 1355},
		{"unknown", 1355}, {"official-24.9", 1354},
	} {
		for _, target := range []uint32{0, 108000520} {
			name := profile.name + "/unselected"
			if target != 0 {
				name = profile.name + "/selected"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				character := dnfrepo.CharacterRecord{
					CharacterID: "3", AccountID: "epic-info-profile", Job: "14", Level: 90,
					Stats: map[string]int64{
						currentEpicProductionTargetStat:      int64(target),
						currentEpicProductionChargeStat:      12345,
						currentEpicProductionCarryGroup1Stat: 111,
						currentEpicProductionCarryGroup2Stat: 222,
					},
				}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				conn := &bufferConn{}
				s := &Service{
					options: options{currentClientVersion: profile.name, gameUpperHeader: gameUpperHeaderServer16,
						gameUpperBodyCodec: gameUpperBodyCodecPlain},
					initialEquipmentArchive: archive,
					repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
				}
				session := &gameSession{conn: conn, accountID: character.AccountID, selectedCharacterID: 3}
				if err := s.sendSelectedCurrentEpicProductionInfo(session, "profile_regression"); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.Classification != 0 || packet.Header.MsgID != profile.want || len(rest) != 0 {
					t.Fatalf("notice class=%d opcode=%d trailing=%d; want class0/%d only",
						packet.Header.Classification, packet.Header.MsgID, len(rest), profile.want)
				}
				// Independent wire expectation, including the unused zero bytes;
				// do not derive it through the production body builder.
				var want bytes.Buffer
				for _, word := range []uint32{3, 7, target, 12345, 0, 111, 222, 0} {
					if err := binary.Write(&want, binary.LittleEndian, word); err != nil {
						t.Fatal(err)
					}
				}
				if conn.write.Len() != 48 || !bytes.Equal(packet.Body, want.Bytes()) {
					t.Fatalf("notice wire length=%d body=%x want body=%x", conn.write.Len(), packet.Body, want.Bytes())
				}
				saved, found, err := repos.Character.Load(ctx, "3")
				if err != nil || !found || !reflect.DeepEqual(saved.Stats, character.Stats) {
					t.Fatalf("notice mutated stats: got=%v want=%v found=%t err=%v", saved.Stats, character.Stats, found, err)
				}
			})
		}
	}
}
