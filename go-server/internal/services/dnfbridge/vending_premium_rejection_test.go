package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFVendingPremiumReasonAndRetry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, expiry := range []int64{0, now.Unix() - 1, now.Unix()} {
		t.Run(strconv.FormatInt(expiry, 10), func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			account := repo.AccountRecord{AccountID: "vending-premium", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(expiry, 10)}}
			actor := repo.CharacterRecord{CharacterID: "77", AccountID: account.AccountID, Stats: map[string]int64{"gold": 999}}
			inventory := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:121": {ItemID: 7454, Count: 2, ExpireAt: now.Add(time.Hour)}}}
			for _, err := range []error{repos.Account.Save(ctx, account), repos.Character.Save(ctx, actor), repos.Inventory.Save(ctx, inventory)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, initialEquipmentArchive: a, gameplayTimers: &processGameplayTimeQueue{now: func() time.Time { return now }}}
			s.repositoryProvider = func() (repo.Group, bool) { return repos, true }
			s.dungeonChoice = func(int) (int, error) { return 0, nil }
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 77, accountID: account.AccountID}
			body := make([]byte, 10)
			binary.LittleEndian.PutUint32(body, 1)
			binary.LittleEndian.PutUint32(body[4:], 1)
			binary.LittleEndian.PutUint16(body[8:], 121)
			for range 2 {
				conn.write.Reset()
				if err := s.handleCurrentVendingMachine(session, body); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.MsgID != 215 || !bytes.Equal(packet.Body, []byte{0, 23}) || len(rest) != 0 {
					t.Fatalf("wrong entitlement result: %x", packet.Body)
				}
				got, _, _ := repos.Inventory.Load(ctx, "77")
				gotAccount, _, _ := repos.Account.Load(ctx, account.AccountID)
				gotActor, _, _ := repos.Character.Load(ctx, "77")
				if !reflect.DeepEqual(inventory, got) || !reflect.DeepEqual(account, gotAccount) || !reflect.DeepEqual(actor, gotActor) {
					t.Fatal("rejection changed assets")
				}
			}
			account.Metadata[premium.MetadataKey(premium.TypeBlackDiamond)] = strconv.FormatInt(now.Unix()+3600, 10)
			if err := repos.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := s.handleCurrentVendingMachine(session, body); err != nil {
				t.Fatal(err)
			}
			packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[3:]) != 1 {
				t.Fatalf("valid entitlement failed: %x", packet.Body)
			}
		})
	}
}
