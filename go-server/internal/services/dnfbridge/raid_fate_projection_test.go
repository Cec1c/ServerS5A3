package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRaidFateProjectionMigratesOfflineOwnerAndRefreshesSharedBalance(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := fateRewardBridgeTime()
	for _, accountID := range []string{"fate-owner", "unrelated-owner"} {
		if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: accountID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, seed := range []struct {
		character, account string
		item               int64
		count              int64
	}{
		{"31", "fate-owner", int64(raidcurrency.FateItemID), 70},
		{"32", "fate-owner", int64(raidcurrency.FateItemID), 90},
		{"33", "unrelated-owner", int64(raidcurrency.FateItemID), 200},
	} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: seed.character, AccountID: seed.account}); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: seed.character, Slots: map[string]repo.ItemStack{"0:121": {ItemID: seed.item, Count: seed.count}, "0:9": {ItemID: 1001, Count: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (repo.Group, bool) { return g, true }, gameplayTimers: &processGameplayTimeQueue{now: func() time.Time { return now }}}
	conn := &bufferConn{}
	se := &gameSession{conn: conn, accountID: "fate-owner", selectedCharacterID: 31}
	if err := s.migrateCurrentFateAccount(ctx, se); err != nil {
		t.Fatal(err)
	}
	account, _, _ := g.Account.Load(ctx, "fate-owner")
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 160 || state.Earned != 0 {
		t.Fatalf("migration state=%+v err=%v", state, err)
	}
	for _, id := range []string{"31", "32"} {
		inv, _, _ := g.Inventory.Load(ctx, id)
		if len(inv.Slots) != 1 || inv.Slots["0:9"].ItemID != 1001 {
			t.Fatalf("migration inventory %s=%+v", id, inv)
		}
	}
	other, _, _ := g.Inventory.Load(ctx, "33")
	if other.Slots["0:121"].Count != 200 {
		t.Fatal("migration touched unrelated account")
	}
	if err := s.migrateCurrentFateAccount(ctx, se); err != nil {
		t.Fatal(err)
	}
	again, _, _ := g.Account.Load(ctx, "fate-owner")
	if !reflect.DeepEqual(account, again) {
		t.Fatal("repeated migration changed account")
	}
	assertSnapshot := func(full bool, wantBalance, wantEarned uint32) {
		t.Helper()
		conn.write.Reset()
		before, _, _ := g.Account.Load(ctx, "fate-owner")
		if err := s.sendCurrentFateSnapshot(ctx, se, full); err != nil {
			t.Fatal(err)
		}
		packets := splitProfessionTrialServer16Packets(t, conn.write.Bytes())
		wantOp := uint16(14)
		if full {
			wantOp = 13
		}
		if len(packets) != 2 || packets[0].Header.MsgID != wantOp || packets[1].Header.MsgID != 1456 || packets[0].Header.Classification != 0 || packets[1].Header.Classification != 0 {
			t.Fatalf("Fate packets=%+v", packets)
		}
		row := packets[0].Body
		if len(row) != 3+0x77 || row[0] != 44 || binary.LittleEndian.Uint16(row[1:3]) != 1 || binary.LittleEndian.Uint16(row[3:5]) != 0 || binary.LittleEndian.Uint32(row[5:9]) != raidcurrency.FateItemID || binary.LittleEndian.Uint32(row[9:13]) != wantBalance {
			t.Fatalf("Fate native list row=%x", row)
		}
		weekly := packets[1].Body
		if len(weekly) != 14 || binary.LittleEndian.Uint16(weekly[:2]) != 1 || binary.LittleEndian.Uint32(weekly[2:6]) != raidcurrency.FateItemID || binary.LittleEndian.Uint32(weekly[6:10]) != wantEarned || binary.LittleEndian.Uint32(weekly[10:14]) != 2500 {
			t.Fatalf("Fate weekly body=%x", weekly)
		}
		after, _, _ := g.Account.Load(ctx, "fate-owner")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("display changed account assets")
		}
	}
	assertSnapshot(true, 160, 0)
	se.selectedCharacterID = 32
	assertSnapshot(true, 160, 0)
	if _, err := raidcurrency.Debit(&account, 40, now); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(false, 120, 0)
	if _, _, err := raidcurrency.Credit(&account, 100, now); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(false, 220, 100)
	conn.write.Reset()
	se.selectedCharacterID = 33
	if err := s.sendCurrentFateSnapshot(ctx, se, true); err == nil || conn.write.Len() != 0 {
		t.Fatalf("mismatched owner emitted balance: err=%v bytes=%d", err, conn.write.Len())
	}
}

type fateBootstrapAccountProbe struct {
	repo.AccountRepository
	loads  int
	failAt int
	err    error
}

func (probe *fateBootstrapAccountProbe) Load(ctx context.Context, accountID string) (repo.AccountRecord, bool, error) {
	probe.loads++
	if probe.failAt > 0 && probe.loads == probe.failAt {
		return repo.AccountRecord{}, false, probe.err
	}
	return probe.AccountRepository.Load(ctx, accountID)
}

func TestCurrentSelectInventoryBootstrapRejectsFateAccountFailuresBeforeAnyWrite(t *testing.T) {
	const (
		accountID   = "dnf:fate-bootstrap-owner"
		characterID = "77"
	)
	injectedErr := errors.New("injected Fate account load failure")
	tests := []struct {
		name               string
		accountPresent     bool
		characterAccountID string
		failAt             int
		wantAccountLoads   int
		wantError          string
	}{
		{
			name:               "unknown_account",
			characterAccountID: accountID,
			wantAccountLoads:   2,
			wantError:          "raid currency account unavailable",
		},
		{
			name:               "character_account_mismatch",
			accountPresent:     true,
			characterAccountID: "dnf:not-the-session-owner",
			wantAccountLoads:   1,
			wantError:          "raid currency owner mismatch",
		},
		{
			name:               "fate_account_read_failure",
			accountPresent:     true,
			characterAccountID: accountID,
			failAt:             2,
			wantAccountLoads:   2,
			wantError:          injectedErr.Error(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories := memory.NewMemoryGroup()
			if test.accountPresent {
				if err := repositories.Account.Save(ctx, repo.AccountRecord{AccountID: accountID}); err != nil {
					t.Fatal(err)
				}
			}
			if err := repositories.Character.Save(ctx, repo.CharacterRecord{
				CharacterID: characterID,
				AccountID:   test.characterAccountID,
			}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(ctx, repo.InventoryRecord{
				CharacterID: characterID,
				Slots:       map[string]repo.ItemStack{},
				Warehouse:   map[string]repo.ItemStack{},
			}); err != nil {
				t.Fatal(err)
			}
			probe := &fateBootstrapAccountProbe{
				AccountRepository: repositories.Account,
				failAt:            test.failAt,
				err:               injectedErr,
			}
			repositories.Account = probe
			service := &Service{
				options:            options{accountPrefix: defaultAccountPrefix},
				repositoryProvider: func() (repo.Group, bool) { return repositories, true },
			}
			connection := &bufferConn{}
			session := &gameSession{
				conn:                connection,
				connID:              "select-inventory-fate-failure-" + test.name,
				accountID:           accountID,
				selectedCharacterID: 77,
			}

			err := service.sendCurrentSelectInventoryBootstrap(session, "fate_account_failure")
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("bootstrap error=%v, want containing %q", err, test.wantError)
			}
			if connection.write.Len() != 0 {
				t.Fatalf("Fate account failure emitted a partial bootstrap: %x", connection.write.Bytes())
			}
			if session.selectedItemListRefreshSent || session.selectedItemListBootstrapCharacterID != 0 {
				t.Fatalf("Fate account failure committed bootstrap state: sent=%t char=%d",
					session.selectedItemListRefreshSent, session.selectedItemListBootstrapCharacterID)
			}
			if probe.loads != test.wantAccountLoads {
				t.Fatalf("account loads=%d, want %d", probe.loads, test.wantAccountLoads)
			}
		})
	}
}
