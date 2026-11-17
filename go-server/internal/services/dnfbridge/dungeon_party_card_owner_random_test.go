package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFPartyCardsUseIndependentOwnerStreamsAndFreezeOnce(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0).UTC()
	owners := []uint16{99, 100, 101, 102}
	run := func(order []uint16) map[uint16]dungeonCardRewardPlan {
		instance := &dungeonPartyInstance{members: map[uint16]time.Time{99: {}, 100: {}, 101: {}, 102: {}}}
		plans := map[uint16]dungeonCardRewardPlan{}
		for _, id := range order {
			s, r, p, _, _ := prepareCompletedSettlementRuntime(t)
			s.dungeonMonsterTable = catalog
			repos, ok := s.repositoryGroup()
			if !ok {
				t.Fatal("repositories missing")
			}
			for _, owner := range owners {
				text := strconv.Itoa(int(owner))
				account := "card-owner-" + text
				if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: account, Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: text, AccountID: account, Level: 1, Job: "0", Stats: map[string]int64{}}); err != nil {
					t.Fatal(err)
				}
			}
			p.selectedCharacterID = id
			p.accountID = "card-owner-" + strconv.Itoa(int(id))
			p.party.state.Members = []alignedcmd.PartyMemberState{{UserID: 99}, {UserID: 100}, {UserID: 101}, {UserID: 102}}
			r.Character, _, err = repos.Character.Load(context.Background(), strconv.Itoa(int(id)))
			if err != nil {
				t.Fatal(err)
			}
			r.AccountID = p.accountID
			r.Seed = 0x12345678
			r.partyInstance = instance
			r.entryPartyCount = 4
			r.Dungeon.Metadata.BasisLevel.Set = true
			r.Dungeon.Metadata.BasisLevel.Value = 1
			r.Request.Difficulty = 1
			r.settlementPlayResultReceived = true
			r.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			r.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
			r.startedAt = now.Add(-13 * time.Second)
			r.clearMapCompletionAt = now
			var roomSeeds []uint32
			for _, visit := range r.Rooms {
				visit.DropRNG = 0x76543210
				roomSeeds = append(roomSeeds, visit.DropRNG)
			}
			if err := s.freezeCurrentDungeonCardRewardStateLocked(p, r); err != nil {
				t.Fatal(err)
			}
			frozen := cloneDungeonCardRewardPlan(r.settlementCardRewardState.plan)
			for _, visit := range r.Rooms {
				if visit.DropRNG != 0x76543210 {
					t.Fatal("party card freeze consumed monster drop stream")
				}
			}
			if len(roomSeeds) == 0 {
				t.Fatal("room fixture missing")
			}
			if err := s.freezeCurrentDungeonCardRewardStateLocked(p, r); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(frozen, r.settlementCardRewardState.plan) {
				t.Fatal("repeat freeze changed rewards")
			}
			plans[id] = frozen
		}
		return plans
	}
	first := run(owners)
	reverse := run([]uint16{102, 101, 100, 99})
	for _, id := range owners {
		// Item expiration is absent for these ordinary equipment rewards.
		if !reflect.DeepEqual(first[id].Sides, reverse[id].Sides) || !reflect.DeepEqual(first[id].BlackDiamondBonus, reverse[id].BlackDiamondBonus) {
			t.Fatalf("request order changed owner %d rewards", id)
		}
	}
	for _, side := range []dungeonCardSide{dungeonCardSideFree, dungeonCardSidePaid} {
		if reflect.DeepEqual(first[100].Sides[side], first[101].Sides[side]) && reflect.DeepEqual(first[101].Sides[side], first[102].Sides[side]) {
			t.Fatalf("all three followers share side %d rewards", side)
		}
	}
	for _, id := range owners {
		if first[id].BlackDiamondBonus.Gold == 0 && len(first[id].BlackDiamondBonus.Items) == 0 {
			t.Fatalf("black diamond owner %d has no bonus", id)
		}
	}
	if reflect.DeepEqual(first[100].BlackDiamondBonus, first[101].BlackDiamondBonus) && reflect.DeepEqual(first[101].BlackDiamondBonus, first[102].BlackDiamondBonus) {
		t.Fatal("all three followers share black diamond rewards")
	}
}
