package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func lotteryFateCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst": "900 `fatejar.stk`\n10304529 `fate.stk`\n",
		"stackable/fatejar.stk":   "[stackable type] `[upgradable legacy]` 0\n[stack limit] 100\n[int data] 10304529 100 30\n[/int data]\n",
		"stackable/fate.stk":      "[stackable type] `[material]` 0\n[attach type] `[account]`\n[stack limit] 1000\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestLotteryFateAccountGrantDeltaAndCap(t *testing.T) {
	for _, initial := range []uint32{0, 70, 2490, 2500} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			account := dnfrepo.AccountRecord{AccountID: "account-1"}
			if initial > 0 {
				if _, _, err := raidcurrency.Credit(&account, initial, boosterFateTestNow()); err != nil {
					t.Fatal(err)
				}
			}
			f := newBoosterFateFixture(t, account, boosterFateBoxSlots(2))
			f.service = currentBoosterTestService(lotteryFateCatalog(t), f.group)
			f.service.options.currentClientVersion = "official-24.9"
			f.service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return f.now }}
			wantBalance := initial
			for open := 1; open <= 2; open++ {
				if err := f.service.handleCurrentLotteryItem(f.session, []byte{0, 0, 75, 0}); err != nil {
					t.Fatal(err)
				}
				frames := drainUpperPackets(t, f.conn)
				grant := min(uint32(30), raidcurrency.WeeklyLimit-wantBalance)
				wantBalance += grant
				if len(frames) != 4 || frames[0].msgID != 27 || len(frames[0].body) != 126 || frames[0].body[0] != 1 {
					t.Fatalf("open=%d frames=%+v", open, frames)
				}
				ack := frames[0].body
				if binary.LittleEndian.Uint16(ack[1:3]) != 75 || binary.LittleEndian.Uint16(ack[3:5]) != 0 ||
					binary.LittleEndian.Uint32(ack[5:9]) != raidcurrency.FateItemID || binary.LittleEndian.Uint32(ack[9:13]) != grant ||
					binary.LittleEndian.Uint32(ack[122:]) != 0 {
					t.Fatalf("open=%d Fate delta row=%x want grant=%d", open, ack, grant)
				}
				if frames[1].msgID != 14 || frames[1].body[0] != 0 || frames[2].msgID != 14 || frames[2].body[0] != 44 ||
					binary.LittleEndian.Uint32(frames[2].body[9:13]) != wantBalance || frames[3].msgID != 1456 ||
					binary.LittleEndian.Uint32(frames[3].body[6:10]) != wantBalance {
					t.Fatalf("committed source/account/weekly packet order=%+v", frames)
				}
				if dir := os.Getenv("DNF_FATE_LOTTERY_EXPORT"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("initial-%d-open-%d.bin", initial, open)), ack, 0600); err != nil {
						t.Fatal(err)
					}
				}
				inv, _, _ := f.group.Inventory.Load(context.Background(), "19")
				if got := inv.Slots["0:75"].Count; got != int64(2-open) {
					t.Fatalf("source count=%d", got)
				}
				assertNoBoosterFateInventoryRow(t, f.group, "19")
				stored, _, _ := f.group.Account.Load(context.Background(), "account-1")
				state, err := raidcurrency.Read(stored, f.now)
				if err != nil || state.Balance != wantBalance || state.Earned != wantBalance {
					t.Fatalf("state=%+v err=%v", state, err)
				}
				if err := f.service.handleCurrentLotteryOverflowConfirm(f.session, []byte{1, 27, 0}); err != nil {
					t.Fatal(err)
				}
				if f.conn.write.Len() != 0 {
					t.Fatal("replayed old confirmation")
				}
			}
		})
	}
}

func lotteryFateAccountWithSeparateState(t *testing.T, balance, earned uint32) dnfrepo.AccountRecord {
	t.Helper()
	if balance > earned {
		t.Fatalf("balance=%d exceeds earned=%d", balance, earned)
	}
	account := dnfrepo.AccountRecord{AccountID: "account-1"}
	if earned > 0 {
		granted, _, err := raidcurrency.Credit(&account, earned, boosterFateTestNow())
		if err != nil || granted != earned {
			t.Fatalf("seed earned=%d granted=%d err=%v", earned, granted, err)
		}
	}
	if balance < earned {
		if _, err := raidcurrency.Debit(&account, earned-balance, boosterFateTestNow()); err != nil {
			t.Fatalf("lower balance to %d: %v", balance, err)
		}
	}
	state, err := raidcurrency.Read(account, boosterFateTestNow())
	if err != nil || state.Balance != balance || state.Earned != earned {
		t.Fatalf("seeded state=%+v want balance=%d earned=%d err=%v", state, balance, earned, err)
	}
	return account
}

func TestLotteryFateUsesEarnedForCapAndBalanceForDelta(t *testing.T) {
	cases := []struct {
		name        string
		balance     uint32
		earned      uint32
		wantGranted uint32
	}{
		{name: "earned-2500-balance-0", balance: 0, earned: 2500, wantGranted: 0},
		{name: "earned-2500-balance-70", balance: 70, earned: 2500, wantGranted: 0},
		{name: "earned-2490-balance-0", balance: 0, earned: 2490, wantGranted: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := lotteryFateAccountWithSeparateState(t, tc.balance, tc.earned)
			f := newBoosterFateFixture(t, account, boosterFateBoxSlots(2))
			f.service = currentBoosterTestService(lotteryFateCatalog(t), f.group)
			f.service.options.currentClientVersion = "official-24.9"
			f.service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return f.now }}
			if err := f.service.handleCurrentLotteryItem(f.session, []byte{0, 0, 75, 0}); err != nil {
				t.Fatal(err)
			}
			frames := drainUpperPackets(t, f.conn)
			if len(frames) != 4 || frames[0].msgID != 27 || len(frames[0].body) != 126 || frames[0].body[0] != 1 {
				t.Fatalf("frames=%+v", frames)
			}
			ack := frames[0].body
			if binary.LittleEndian.Uint16(ack[1:3]) != 75 ||
				binary.LittleEndian.Uint32(ack[5:9]) != raidcurrency.FateItemID ||
				binary.LittleEndian.Uint32(ack[9:13]) != tc.wantGranted ||
				binary.LittleEndian.Uint32(ack[122:]) != 0 {
				t.Fatalf("Fate ACK=%x want granted=%d", ack, tc.wantGranted)
			}
			wantBalance := tc.balance + tc.wantGranted
			if frames[1].msgID != 14 || frames[1].body[0] != 0 ||
				frames[2].msgID != 14 || frames[2].body[0] != 44 ||
				binary.LittleEndian.Uint32(frames[2].body[9:13]) != wantBalance ||
				frames[3].msgID != 1456 ||
				binary.LittleEndian.Uint32(frames[3].body[6:10]) != raidcurrency.WeeklyLimit {
				t.Fatalf("committed source/account/weekly frames=%+v want balance=%d", frames, wantBalance)
			}
			inventory, found, err := f.group.Inventory.Load(context.Background(), "19")
			if err != nil || !found || inventory.Slots["0:75"].Count != 1 {
				t.Fatalf("source after open found=%t inventory=%+v err=%v", found, inventory, err)
			}
			stored, found, err := f.group.Account.Load(context.Background(), "account-1")
			if err != nil || !found {
				t.Fatalf("account found=%t err=%v", found, err)
			}
			state, err := raidcurrency.Read(stored, f.now)
			if err != nil || state.Balance != wantBalance || state.Earned != raidcurrency.WeeklyLimit {
				t.Fatalf("state=%+v want balance=%d earned=%d err=%v", state, wantBalance, raidcurrency.WeeklyLimit, err)
			}
			assertNoBoosterFateInventoryRow(t, f.group, "19")
		})
	}
}

func lotteryFateRewardGroups(entries ...alignedcmd.MagicBoxRewardEntry) []alignedcmd.MagicBoxRewardGroup {
	groups := make([]alignedcmd.MagicBoxRewardGroup, 0, len(entries))
	for _, entry := range entries {
		groups = append(groups, alignedcmd.MagicBoxRewardGroup{
			DrawCount: 1,
			Entries:   []alignedcmd.MagicBoxRewardEntry{entry},
		})
	}
	return groups
}

var errLotteryFateMutationReached = errors.New("lottery Fate mutation unexpectedly reached")

type lotteryFateMutationProbe struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	called bool
}

func (p *lotteryFateMutationProbe) WithinAccountCharacterLedgerAssets(
	context.Context,
	string,
	string,
	func(
		dnfrepo.AccountRepository,
		dnfrepo.AccountInventoryRepository,
		dnfrepo.CharacterRepository,
		dnfrepo.InventoryRepository,
		dnfrepo.EquipmentRepository,
	) error,
) error {
	p.called = true
	return errLotteryFateMutationReached
}

func TestLotteryFateRejectsMixedAndNonOfficialBeforeMutation(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		contract bool
		rewards  []alignedcmd.MagicBoxRewardEntry
	}{
		{
			name:    "official-fate-plus-item",
			version: "official-24.9",
			rewards: []alignedcmd.MagicBoxRewardEntry{
				{ItemID: int64(raidcurrency.FateItemID), Weight: 1, Count: 30},
				{ItemID: 600, Weight: 1, Count: 1},
			},
		},
		{
			name:    "official-fate-plus-gold",
			version: "official-24.9",
			rewards: []alignedcmd.MagicBoxRewardEntry{
				{ItemID: int64(raidcurrency.FateItemID), Weight: 1, Count: 30},
				{ItemID: 0, Weight: 1, Count: 100},
			},
		},
		{
			name:     "official-fate-plus-contract",
			version:  "official-24.9",
			contract: true,
			rewards: []alignedcmd.MagicBoxRewardEntry{
				{ItemID: int64(raidcurrency.FateItemID), Weight: 1, Count: 30},
				{ItemID: 600, Weight: 1, Count: 1},
			},
		},
		{
			name:    "non-official-pure-fate",
			version: "official-24.8",
			rewards: []alignedcmd.MagicBoxRewardEntry{
				{ItemID: int64(raidcurrency.FateItemID), Weight: 1, Count: 30},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBoosterFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, boosterFateBoxSlots(2))
			character, found, err := f.group.Character.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("seed character found=%t err=%v", found, err)
			}
			if character.Stats == nil {
				character.Stats = make(map[string]int64)
			}
			character.Stats["gold"] = 5000
			if err := f.group.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			catalog := lotteryFateCatalog(t)
			f.service = currentBoosterTestService(catalog, f.group)
			f.service.options.currentClientVersion = tc.version
			f.service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return f.now }}
			contracts := make(map[int64]currentPremiumContractInfo)
			if tc.contract {
				contracts[600] = currentPremiumContractInfo{ItemID: 600, PremiumType: premium.TypeOverEquip, DurationSeconds: 86400}
			}
			f.service.premiumCatalog = &currentPremiumCatalog{contractsByItem: contracts}

			definition, err := resolveCurrentBoosterDefinition(catalog, boosterFateTestBoxItemID, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			definition.Random = alignedcmd.MagicBoxResolution{Kind: "random", Groups: lotteryFateRewardGroups(tc.rewards...)}
			probe := &lotteryFateMutationProbe{AccountCharacterAssetUnitOfWork: f.group.AccountAssets}
			f.group.AccountAssets = probe
			f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.group, true }

			beforeAccount, found, err := f.group.Account.Load(context.Background(), "account-1")
			if err != nil || !found {
				t.Fatalf("before account found=%t err=%v", found, err)
			}
			beforeInventory, found, err := f.group.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("before inventory found=%t err=%v", found, err)
			}
			beforeCharacter, found, err := f.group.Character.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("before character found=%t err=%v", found, err)
			}
			_, err = f.service.commitCurrentBooster(context.Background(), f.session, catalog, definition, currentBoosterOpenRequest{
				AllowLotteryGold: true,
				Kind:             currentBoosterRequestRandom,
				SourceSlot:       75,
				RewardMultiplier: 1,
			})
			if !errors.Is(err, errCurrentBoosterPVFInvalid) {
				t.Fatalf("commit error=%v, want PVF/projection rejection", err)
			}
			if probe.called {
				t.Fatal("unsupported reward reached an asset transaction")
			}
			afterAccount, _, _ := f.group.Account.Load(context.Background(), "account-1")
			afterInventory, _, _ := f.group.Inventory.Load(context.Background(), "19")
			afterCharacter, _, _ := f.group.Character.Load(context.Background(), "19")
			if !reflect.DeepEqual(beforeAccount, afterAccount) ||
				!reflect.DeepEqual(beforeInventory, afterInventory) ||
				!reflect.DeepEqual(beforeCharacter, afterCharacter) {
				t.Fatal("unsupported reward changed source, ledger or gold")
			}
		})
	}
}

func TestLotteryFateFailureRollsBackAndFullBagNeedsNoMainSlot(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newBoosterFateFixture(t, dnfrepo.AccountRecord{}, fullBoosterFateBagExceptSource())
			beforeAccount, _, _ := f.group.Account.Load(context.Background(), "account-1")
			beforeInventory, _, _ := f.group.Inventory.Load(context.Background(), "19")
			if fail {
				ledger := f.group.AccountAssets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork)
				f.group.AccountAssets = boosterFateFailingLedger{AccountCharacterAssetUnitOfWork: f.group.AccountAssets, ledger: ledger}
			}
			f.service = currentBoosterTestService(lotteryFateCatalog(t), f.group)
			f.service.options.currentClientVersion = "official-24.9"
			f.service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return f.now }}
			if err := f.service.handleCurrentLotteryItem(f.session, []byte{0, 0, 75, 0}); err != nil {
				t.Fatal(err)
			}
			frames := drainUpperPackets(t, f.conn)
			afterAccount, _, _ := f.group.Account.Load(context.Background(), "account-1")
			afterInventory, _, _ := f.group.Inventory.Load(context.Background(), "19")
			if fail {
				if len(frames) != 1 || frames[0].msgID != 27 || !bytes.Equal(frames[0].body, []byte{0, 0x13}) ||
					!reflect.DeepEqual(beforeAccount, afterAccount) || !reflect.DeepEqual(beforeInventory, afterInventory) {
					t.Fatal("failed grant leaked assets or success packet")
				}
			} else {
				state, err := raidcurrency.Read(afterAccount, f.now)
				if err != nil || state.Balance != 30 || afterInventory.Slots["0:75"].Count != 0 || len(frames) != 4 {
					t.Fatalf("full bag grant state=%+v frames=%+v err=%v", state, frames, err)
				}
			}
			assertNoBoosterFateInventoryRow(t, f.group, "19")
		})
	}
}
