package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestEpicProductionInfoCommittedCountSnapshotProfiles(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"", "90cn", "legacy", "unknown", "official-24.9"} {
		for _, tc := range []struct {
			name      string
			used      int64
			stale     bool
			wire      byte
			remaining uint32
		}{
			{"four_used", 4, false, 4, 3}, {"exhausted", 7, false, 7, 0},
			{"rollover", 7, true, 0, 7}, {"negative", -1, false, 0, 7},
			{"over_limit_record", 255, false, 7, 0}, {"legacy_wide_record", 256, false, 7, 0},
		} {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				week := currentEpicProductionWeekStart(time.Now().UTC())
				if tc.stale {
					week -= 7 * 24 * 60 * 60
				}
				before := dnfrepo.CharacterRecord{CharacterID: "3", AccountID: "info-count", Level: 90, Stats: map[string]int64{
					currentEpicProductionTargetStat: 108000520, currentEpicProductionChargeStat: 12345,
					currentEpicProductionWeeklyCountStat: tc.used, currentEpicProductionWeekStartStat: week,
					currentEpicProductionCarryGroup1Stat: 111, currentEpicProductionCarryGroup2Stat: 222,
				}}
				if err := repos.Character.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				conn := &bufferConn{}
				s := &Service{options: options{currentClientVersion: profile, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				if err := s.sendSelectedCurrentEpicProductionInfo(&gameSession{conn: conn, accountID: before.AccountID, selectedCharacterID: 3}, "count_snapshot"); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				var expected bytes.Buffer
				for _, n := range []uint32{3, tc.remaining, 108000520, 12345, 0, 111, 222, 0} {
					_ = binary.Write(&expected, binary.LittleEndian, n)
				}
				want := expected.Bytes()
				id := uint16(1355)
				if profile == "official-24.9" {
					id, want[17] = 1354, tc.wire
				}
				if packet.Header.Classification != 0 || packet.Header.MsgID != id || len(rest) != 0 || !bytes.Equal(packet.Body, want) {
					t.Fatalf("notice id=%d body=%x want id=%d body=%x", packet.Header.MsgID, packet.Body, id, want)
				}
				after, found, err := repos.Character.Load(ctx, "3")
				if err != nil || !found || !reflect.DeepEqual(before.Stats, after.Stats) {
					t.Fatal("read-only notice modified persisted state")
				}
			})
		}
	}
}
