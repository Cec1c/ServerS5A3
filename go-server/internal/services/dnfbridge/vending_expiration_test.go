package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Exact machine 1 and currency 7454 are read from the deployed 369d PVF.
// The inventory and entitlement are isolated fixtures, never production rows.
func TestRealPVFVendingCoinExpirationBoundaries(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires deployed PVF")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, tc := range []struct {
		name     string
		expiry   time.Time
		extra    map[string]string
		accepted bool
		machine  uint32
		coin     int64
	}{
		{"ordinary_coin_without_deadline", time.Time{}, nil, true, 1, 7454},
		{"expired_instance", now.Add(-time.Hour), nil, false, 1, 7454},
		{"future_instance", now.Add(time.Hour), nil, true, 1, 7454},
		{"expired_wire_metadata", time.Time{}, map[string]string{"expire_time": strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)}, false, 1, 7454},
		{"future_wire_metadata", time.Time{}, map[string]string{"expire_unix": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}, true, 1, 7454},
		{"historical_pvf_deadline_no_instance", time.Time{}, nil, false, 2, 10005506},
		{"historical_pvf_deadline_future_instance", now.Add(time.Hour), nil, true, 2, 10005506},
		{"historical_pvf_deadline_expired_instance", now.Add(-time.Hour), nil, false, 2, 10005506},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			must(repos.Account.Save(ctx, repo.AccountRecord{AccountID: "expiration-fixture", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}))
			must(repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "expiration-fixture", Level: 90}))
			before := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:121": {ItemID: tc.coin, Count: 1, ExpireAt: tc.expiry, Extra: tc.extra}}}
			must(repos.Inventory.Save(ctx, before))
			service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, initialEquipmentArchive: a}
			service.repositoryProvider = func() (repo.Group, bool) { return repos, true }
			service.dungeonChoice = func(int) (int, error) { return 0, nil }
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 77, accountID: "expiration-fixture"}
			body := make([]byte, 10)
			binary.LittleEndian.PutUint32(body, tc.machine)
			binary.LittleEndian.PutUint32(body[4:], 1)
			binary.LittleEndian.PutUint16(body[8:], 121)
			must(service.handleCurrentVendingMachine(session, body))
			packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
			if len(packets) == 0 || packets[0].Header.MsgID != 215 || (packets[0].Body[0] == 1) != tc.accepted {
				t.Fatalf("unexpected vending response: %+v", packets)
			}
			after, found, e := repos.Inventory.Load(ctx, "77")
			must(e)
			if !found {
				t.Fatal("inventory missing")
			}
			if !tc.accepted && (!reflect.DeepEqual(before, after) || len(packets) != 1) {
				t.Fatalf("expired source produced asset/refresh mutation: %+v", after)
			}
			if tc.accepted && after.Slots["0:121"].Count != 0 {
				t.Fatal("valid coin not debited")
			}
		})
	}
}
