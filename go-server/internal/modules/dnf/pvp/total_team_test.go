package pvp

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestTotalTeamOwnershipProfessionAndStableSlots(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Now()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "account", Metadata: map[string]string{"keep": "value"}}); err != nil {
		t.Fatal(err)
	}
	rows := []repo.CharacterRecord{}
	for i := 0; i < 3; i++ {
		rows = append(rows, repo.CharacterRecord{CharacterID: fmt.Sprint(i + 1), AccountID: "account", Slot: i, Name: fmt.Sprint("role", i), Job: fmt.Sprint(i), Level: 90, Stats: map[string]int64{"grow_type": 17}})
	}
	source := textSource{"etc/pvpparameter.etc": "[pvp matching possible character]\n0 1 1 1 2 1\n[/pvp matching possible character]"}
	team, err := ResolveTotalTeam("account", "王者队伍", [3]byte{0, 1, 2}, rows, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rows {
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveTotalTeam(ctx, repos.Account, "account", team.Team, now); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][3]byte{{0, 0, 2}, {0, 1, 255}} {
		if _, err := ResolveTotalTeam("account", "王者队伍", bad, rows, source); err == nil {
			t.Fatal("invalid roster accepted", bad)
		}
	}
	if _, err := ResolveTotalTeam("other", "王者队伍", [3]byte{0, 1, 2}, rows, source); err == nil {
		t.Fatal("foreign characters accepted")
	}
	duplicate := append([]repo.CharacterRecord(nil), rows...)
	duplicate[1].Job = "0"
	if _, err := ResolveTotalTeam("account", "王者队伍", [3]byte{0, 1, 2}, duplicate, source); err == nil {
		t.Fatal("same profession accepted")
	}
	rows[0].Slot, rows[2].Slot = 2, 0
	for _, c := range rows {
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := ReadTotalTeam(ctx, repos, "account")
	if err != nil || loaded.Team.Characters != [3]string{"1", "2", "3"} || loaded.Slots != [3]byte{2, 1, 0} {
		t.Fatalf("slot reorder rebound team: %+v %v", loaded, err)
	}
	rows[1].Stats["delete_flag"] = 1
	if err := repos.Character.Save(ctx, rows[1]); err != nil {
		t.Fatal(err)
	}
	loaded, err = ReadTotalTeam(ctx, repos, "account")
	if err != nil || loaded.Slots[1] != 255 {
		t.Fatal("deleted character retained", loaded, err)
	}
	account, _, _ := repos.Account.Load(ctx, "account")
	if account.Metadata["keep"] != "value" {
		t.Fatal("unrelated metadata overwritten")
	}
	finder := repos.Account.(repo.PVPTotalTeamNameFinder)
	id, found, err := finder.FindAccountIDByPVPTotalTeamName(ctx, "王者队伍")
	if err != nil || !found || id != "account" {
		t.Fatal(id, found, err)
	}
}

func TestTotalTeamRenameConsumesOnceAndRejectsForeignExpiredWrongCard(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, scenario := range []string{"valid", "wrong", "expired", "foreign", "locked", "no_team"} {
		t.Run(scenario, func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
				t.Fatal(err)
			}
			team := TotalTeam{Name: "原来队伍", Characters: [3]string{"1", "2", "3"}}
			if scenario != "no_team" {
				if err := SaveTotalTeam(ctx, repos.Account, "a", team, now); err != nil {
					t.Fatal(err)
				}
			}
			owner := "a"
			if scenario == "foreign" {
				owner = "b"
			}
			if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: owner}); err != nil {
				t.Fatal(err)
			}
			card := repo.ItemStack{ItemID: TotalTeamRenameCardID, Count: 2}
			if scenario == "wrong" {
				card.ItemID++
			}
			if scenario == "expired" {
				card.ExpireAt = now
			}
			if scenario == "locked" {
				card.Extra = map[string]string{"equipment_lock_state": "locked"}
			}
			if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:100": card}}); err != nil {
				t.Fatal(err)
			}
			err := RenameTotalTeam(ctx, repos, "a", "1", "新的队伍", 100, now)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if err := RenameTotalTeam(ctx, repos, "a", "1", "新的队伍", 100, now); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid rename accepted")
			}
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			want := int64(2)
			if scenario == "valid" {
				want = 1
			}
			if inv.Slots["0:100"].Count != want {
				t.Fatalf("card count=%d want=%d", inv.Slots["0:100"].Count, want)
			}
		})
	}
}
