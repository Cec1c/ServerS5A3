package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfcerashop "longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	blackDiamondShopPackageID   uint32 = 490709931
	blackDiamondShopVoucherID   uint32 = 490709930
	blackDiamondShopAccountID          = "black-diamond-shop-account"
	blackDiamondShopCharacterID uint16 = 19
)

type blackDiamondShopFixture struct {
	shop         *pvfCeraShopCatalog
	service      *Service
	repositories dnfrepo.Group
	connection   *bufferConn
	session      *gameSession
}

func newBlackDiamondShopFixture(t *testing.T, tokenCera, cera, gold int64) *blackDiamondShopFixture {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_BLACK_DIAMOND_PVF"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_BLACK_DIAMOND_PVF to the patched runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load patched PVF: %v", err)
	}
	shop, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatalf("load real Cera shop catalog: %v", err)
	}
	premiumCatalog, err := buildCurrentPremiumCatalog(archive)
	if err != nil {
		t.Fatalf("load real premium catalog: %v", err)
	}

	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	account := dnfrepo.AccountRecord{AccountID: blackDiamondShopAccountID}
	dnfcerashop.SetBalance(&account, cera)
	dnfcerashop.SetTokenCeraBalance(&account, tokenCera)
	dnfcerashop.SetHappyTokenCeraBalance(&account, 0)
	if err := repositories.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   blackDiamondShopAccountID,
		Stats:       map[string]int64{"gold": gold, "cera": cera},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}

	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          blackDiamondShopAccountID,
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		initialEquipmentArchive: archive,
		ceraShopCatalog:         shop,
		pvfItemCatalog:          shop.items,
		premiumCatalog:          premiumCatalog,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}
	session := &gameSession{
		conn:                connection,
		connID:              t.Name(),
		accountID:           blackDiamondShopAccountID,
		selectedCharacterID: blackDiamondShopCharacterID,
		characterGeneration: 1,
	}
	service.mu.Lock()
	service.gameSessions = map[uint16]*gameSession{blackDiamondShopCharacterID: session}
	service.mu.Unlock()
	return &blackDiamondShopFixture{
		shop:         shop,
		service:      service,
		repositories: repositories,
		connection:   connection,
		session:      session,
	}
}

func blackDiamondShopOpenBody(slot int16) []byte {
	body := make([]byte, currentCeraPackageRequestHeaderSize)
	binary.LittleEndian.PutUint16(body[:2], uint16(slot))
	return body
}

func (f *blackDiamondShopFixture) wallet(t *testing.T) dnfcerashop.Wallet {
	t.Helper()
	account, found, err := f.repositories.Account.Load(context.Background(), blackDiamondShopAccountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	return dnfcerashop.WalletBalance(account)
}

func (f *blackDiamondShopFixture) gold(t *testing.T) int64 {
	t.Helper()
	character, found, err := f.repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	return character.Stats["gold"]
}

func (f *blackDiamondShopFixture) characterCera(t *testing.T) int64 {
	t.Helper()
	character, found, err := f.repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	return character.Stats["cera"]
}

func (f *blackDiamondShopFixture) inventory(t *testing.T) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := f.repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return inventory
}

func (f *blackDiamondShopFixture) premiumExpiry(t *testing.T) int64 {
	t.Helper()
	account, found, err := f.repositories.Account.Load(context.Background(), blackDiamondShopAccountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	return premium.ExpireAt(account, premium.TypeBlackDiamond)
}

func blackDiamondShopMainStack(t *testing.T, inventory dnfrepo.InventoryRecord, itemID uint32) (int16, dnfrepo.ItemStack) {
	t.Helper()
	var (
		slot  int16 = -1
		stack dnfrepo.ItemStack
	)
	for key, candidate := range inventory.Slots {
		if candidate.ItemID != int64(itemID) || candidate.Count <= 0 {
			continue
		}
		listType, candidateSlot, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != dnfrepo.MainInventoryListType {
			t.Fatalf("item=%d appeared outside the main inventory: key=%q", itemID, key)
		}
		if slot >= 0 {
			t.Fatalf("item=%d has duplicate main-inventory rows at slots %d and %d", itemID, slot, candidateSlot)
		}
		slot, stack = candidateSlot, candidate
	}
	if slot < 0 {
		t.Fatalf("item=%d is absent from the main inventory: %+v", itemID, inventory.Slots)
	}
	return slot, stack
}

// Seed a selected gift package directly; merchant shelves/pricing are outside this publication.
func (f *blackDiamondShopFixture) seedPackage(t *testing.T) {
	t.Helper()
	inv := f.inventory(t)
	if inv.Slots == nil {
		inv.Slots = map[string]dnfrepo.ItemStack{}
	}
	inv.Slots["0:0"] = dnfrepo.ItemStack{ItemID: int64(blackDiamondShopPackageID), Count: 1, Extra: map[string]string{"instance_id": "49070931"}}
	if err := f.repositories.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
}

func (f *blackDiamondShopFixture) openPackage(t *testing.T) int16 {
	t.Helper()
	packageSlot, packageStack := blackDiamondShopMainStack(t, f.inventory(t), blackDiamondShopPackageID)
	if packageStack.Count != 1 {
		t.Fatalf("purchased package count=%d, want 1", packageStack.Count)
	}
	f.connection.write.Reset()
	if err := f.service.handleGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOpenCerapackage),
		blackDiamondShopOpenBody(packageSlot),
	); err != nil {
		t.Fatalf("open purchased package at slot %d: %v", packageSlot, err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.connection.write.Bytes())
	want := []byte{1, byte(packageSlot), byte(uint16(packageSlot) >> 8)}
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketOpenCerapackage) ||
		!bytes.Equal(ack.Body, want) {
		t.Fatalf("package ACK header=%+v body=%x want=%x", ack.Header, ack.Body, want)
	}
	return packageSlot
}

func (f *blackDiamondShopFixture) useVoucher(t *testing.T) (int64, int64) {
	t.Helper()
	slot, stack := blackDiamondShopMainStack(t, f.inventory(t), blackDiamondShopVoucherID)
	resolver, err := f.service.alignedPremiumContractResolverForCommand(dnfenum.CmdPacketUseStackable)
	if err != nil || resolver == nil {
		t.Fatalf("resolve real premium contract: resolver=%t err=%v", resolver != nil, err)
	}
	contract, err := resolver(int64(blackDiamondShopVoucherID))
	if err != nil {
		t.Fatalf("resolve voucher %d: %v", blackDiamondShopVoucherID, err)
	}
	if contract.ItemID != int64(blackDiamondShopVoucherID) ||
		contract.PremiumType != premium.TypeBlackDiamond ||
		contract.DurationSeconds != 30*86400 {
		t.Fatalf("real voucher contract=%+v", contract)
	}

	owner, err := dnfinventory.NewOwner(f.repositories)
	if err != nil {
		t.Fatal(err)
	}
	command := dnfinventory.NewUseStackableCommand(
		alignedcmd.Request{
			AccountID:           blackDiamondShopAccountID,
			SelectedCharacterID: blackDiamondShopCharacterID,
		},
		dnfinventory.UseStackableRequest{
			SlotIndex:     slot,
			ListType:      dnfrepo.MainInventoryListType,
			InstanceValue: 0x49070930,
			ItemCode:      int32(stack.ItemID),
		},
	)
	result, err := owner.UseStackable(context.Background(), command, resolver)
	if err != nil || !result.PremiumActivated || result.PremiumType != premium.TypeBlackDiamond {
		t.Fatalf("use voucher result=%+v err=%v", result, err)
	}
	if _, found := blackDiamondShopMainStackOptional(f.inventory(t), blackDiamondShopVoucherID); found {
		t.Fatal("actively used voucher remained in the inventory")
	}
	return result.RemainingCount, f.premiumExpiry(t)
}

func blackDiamondShopMainStackOptional(inventory dnfrepo.InventoryRecord, itemID uint32) (dnfrepo.ItemStack, bool) {
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(itemID) && stack.Count > 0 {
			return stack, true
		}
	}
	return dnfrepo.ItemStack{}, false
}
