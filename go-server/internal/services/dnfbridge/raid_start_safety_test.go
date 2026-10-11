package dnfbridge

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type raidFailGroupTransaction struct {
	dnfrepo.CharacterTradeUnitOfWork
	group         dnfrepo.CharacterInventoryGroupUnitOfWork
	failID        string
	commitFailure bool
}

func (u raidFailGroupTransaction) WithinCharacterInventoryGroup(ctx context.Context, ids []string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository) error) error {
	return u.group.WithinCharacterInventoryGroup(ctx, ids, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository) error {
		if err := apply(c, raidFailGroupSave{i, u.failID}); err != nil {
			return err
		}
		if u.commitFailure {
			return errors.New("injected group commit failure")
		}
		return nil
	})
}

type raidFailGroupSave struct {
	dnfrepo.InventoryRepository
	failID string
}

func (i raidFailGroupSave) Save(ctx context.Context, r dnfrepo.InventoryRecord) error {
	if err := i.InventoryRepository.Save(ctx, r); err != nil {
		return err
	}
	if r.CharacterID == i.failID {
		return errors.New("injected late member save failure")
	}
	return nil
}

type raidFailRead struct{ dnfrepo.InventoryRepository }

func (i raidFailRead) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	return dnfrepo.InventoryRecord{}, false, errors.New("injected inventory read failure")
}

func raidSafetyFixture(t *testing.T, archive *platformpvf.Archive, members int) *raidStartGuardFixture {
	t.Helper()
	f := newRaidStartGuardFixture(t, archive, map[string]dnfrepo.ItemStack{"0:50": {ItemID: raidStartGuardTestMaterial, Count: 3}})
	t.Cleanup(func() { _ = f.logger.Close() })
	for n := 1; n < members; n++ {
		id := strconv.Itoa(19 + n)
		if err := f.repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id}); err != nil {
			t.Fatal(err)
		}
		if err := f.repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{"0:50": {ItemID: raidStartGuardTestMaterial, Count: 3}}}); err != nil {
			t.Fatal(err)
		}
		f.state.Members = append(f.state.Members, runtimeRaidMemberState{CharID: uint16(19 + n)})
	}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.repos, true }
	return f
}

func TestRaidStartSafetyRegressions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"non_leader", "repeated_start", "commit_failure", "late_member_failure", "read_failure", "oversized_group", "rule_failure", "wrong_account", "missing_group_transaction", "duplicate_member", "normal_mode_minimum", "concurrent_start", "valid_team", "normal_team", "normal_late_member_failure"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(raidStartEntryCostGuardEnv, "enforce")
			n := 1
			if name == "non_leader" || name == "late_member_failure" || name == "valid_team" {
				n = 2
			}
			if name == "oversized_group" {
				n = 5
			}
			if name == "normal_team" || name == "normal_late_member_failure" {
				n = 10
			}
			f := raidSafetyFixture(t, archive, n)
			session := f.session()
			if n == 10 {
				f.state.TailFlag = 0
				for i := range f.state.Members {
					f.state.Members[i].GroupIndex = byte(i/runtimeRaidGroupSize + 1)
					f.state.Members[i].SlotOrder = byte(i % runtimeRaidGroupSize)
				}
			}
			switch name {
			case "normal_mode_minimum":
				f.state.TailFlag = 0
			case "non_leader":
				session.selectedCharacterID = 20
				session.accountID = "account-20"
			case "wrong_account":
				session.accountID = "other-account"
			case "rule_failure":
				f.service.initialEquipmentArchive = nil
				session.channel.Type = 0
			case "read_failure":
				f.repos.Inventory = raidFailRead{f.repos.Inventory}
			case "missing_group_transaction":
				f.repos.CharacterTrade = nil
			case "duplicate_member":
				f.state.Members = append(f.state.Members, f.state.Members[0])
			case "commit_failure", "late_member_failure", "normal_late_member_failure":
				u := raidFailGroupTransaction{CharacterTradeUnitOfWork: f.repos.CharacterTrade, group: f.repos.CharacterTrade.(dnfrepo.CharacterInventoryGroupUnitOfWork), commitFailure: name == "commit_failure"}
				if name == "late_member_failure" {
					u.failID = "20"
				}
				if name == "normal_late_member_failure" {
					u.failID = "28"
				}
				f.repos.CharacterTrade = u
			}
			successes := 0
			if name == "concurrent_start" {
				var wg sync.WaitGroup
				results := make(chan bool, 8)
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); _, ok, _ := f.service.startRuntimeRaidWithEntryCost(session); results <- ok }()
				}
				wg.Wait()
				close(results)
				for ok := range results {
					if ok {
						successes++
					}
				}
			} else {
				_, ok, _ := f.service.startRuntimeRaidWithEntryCost(session)
				if ok {
					successes++
				}
				if name == "repeated_start" {
					_, ok, _ = f.service.startRuntimeRaidWithEntryCost(session)
					if ok {
						successes++
					}
				}
			}
			want := int64(3)
			wantStarts := 0
			if name == "valid_team" || name == "normal_team" || name == "repeated_start" || name == "concurrent_start" {
				want = 2
				wantStarts = 1
			}
			if successes != wantStarts || f.state.Started != (wantStarts == 1) {
				t.Fatalf("starts=%d Started=%v want=%d", successes, f.state.Started, wantStarts)
			}
			for _, member := range f.state.Members {
				// The read-failure wrapper is intentionally only the preflight reader.
				bags := f.repos.Inventory
				if failed, ok := bags.(raidFailRead); ok {
					bags = failed.InventoryRepository
				}
				inventory, found, err := bags.Load(context.Background(), strconv.Itoa(int(member.CharID)))
				if err != nil || !found || inventory.Slots["0:50"].Count != want {
					t.Fatalf("member=%d count=%d want=%d found=%v err=%v", member.CharID, inventory.Slots["0:50"].Count, want, found, err)
				}
			}
		})
	}
}

func TestRaidStartLogAndOffKeepSuccessBytesAndNeverConsume(t *testing.T) {
	var baseline []byte
	for _, mode := range []string{"off", "log"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(raidStartEntryCostGuardEnv, mode)
			f := raidSafetyFixture(t, nil, 1)
			session := f.session()
			f.service.bindGameSessionCharacter(session, 19)
			if err := f.service.handleOnlineStartRaid(session, 666, nil); err != nil {
				t.Fatal(err)
			}
			if !f.state.Started {
				t.Fatal("valid observational start rejected")
			}
			if count, _ := f.slotCount(t, "0:50"); count != 3 {
				t.Fatalf("observational mode consumed %d", 3-count)
			}
			got := append([]byte(nil), f.conn.write.Bytes()...)
			if mode == "off" {
				baseline = got
			} else if string(got) != string(baseline) {
				t.Fatalf("log bytes differ from off: %x vs %x", got, baseline)
			}
		})
	}
}
