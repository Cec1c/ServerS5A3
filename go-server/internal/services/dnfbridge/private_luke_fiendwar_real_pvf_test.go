package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/raid"
)

// Force the outer token branch and a selected native weighted boundary while
// leaving both actual PVF container expansions on their first valid outcome.
func privateLukeFiendwarRealTokenRoll(t *testing.T, catalog *pvfDungeonDropCatalog, flow *currentAntonRewardFlow, nativeDraw uint32) func(int64) (int64, error) {
	t.Helper()
	id := uint32(990027102)
	if flow.Mode == 3 {
		id = 990027103
	}
	pool, total, err := flow.Rewards.Pool("squad_item", flow.Rank)
	if err != nil {
		t.Fatal(err)
	}
	var start, tokenStart, tokenWeight uint32
	var choice raid.AntonRewardChoice
	found := 0
	for _, row := range pool {
		if row.ContainerID == id {
			choice, tokenStart, tokenWeight = row, start, row.Weight
			found++
		}
		start += row.Weight
	}
	if found != 1 || uint64(tokenWeight)*10 != uint64(total) || nativeDraw >= total-tokenWeight {
		t.Fatalf("actual private pool differs: mode=%d rank=%d token=%d total=%d matches=%d", flow.Mode, flow.Rank, tokenWeight, total, found)
	}
	tokenCalls := 0
	base, err := resolveCurrentAntonConfiguredReward(catalog, choice, func(int64) (int64, error) { tokenCalls++; return 0, nil }, flow.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	amount := uint32(4000)
	if flow.Mode == 3 {
		amount = 5000
	}
	privateRaidAssertContents(t, base.Rewards, map[uint32]uint32{2681917: amount})
	calls := 0
	return func(n int64) (int64, error) {
		calls++
		if calls == 1 {
			if n != int64(total) {
				t.Fatalf("outer weight=%d want %d", n, total)
			}
			return int64(tokenStart), nil
		}
		if calls == 2+tokenCalls {
			if n != int64(total-tokenWeight) {
				t.Fatalf("native weight=%d want %d", n, total-tokenWeight)
			}
			return int64(nativeDraw), nil
		}
		return 0, nil
	}
}

func TestRealPVFPrivateLukeFiendwarEveryNativeBranch(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	for _, mode := range []byte{2, 3} {
		config, err := raid.LoadLukeRewardConfiguration(catalog.source)
		if mode == 3 {
			config, err = raid.LoadFiendWarRewardConfiguration(catalog.source)
		}
		if err != nil {
			t.Fatal(err)
		}
		for rank := byte(0); rank < 4; rank++ {
			seed, _ := privateLukeFiendwarFlow(mode)
			seed.Rank, seed.Rewards = rank, config.Phases[seed.Phase]
			pool, _, err := seed.Rewards.Pool("squad_item", rank)
			if err != nil {
				t.Fatal(err)
			}
			var nativeStart uint32
			for _, choice := range pool {
				if choice.ContainerID == 990027102 || choice.ContainerID == 990027103 {
					continue
				}
				for _, draw := range []uint32{nativeStart, nativeStart + choice.Weight - 1} {
					t.Run(fmt.Sprintf("mode%d/rank%d/native%d/draw%d", mode, rank, choice.ContainerID, draw), func(t *testing.T) {
						f, owner := privateLukeFiendwarFlow(mode)
						f.Rank, f.Rewards = rank, seed.Rewards
						owner.Clears = f.Rewards.MinimumClears
						native, err := resolveCurrentAntonConfiguredReward(catalog, choice, zeroAntonRoll, f.CompletedAt)
						if err != nil || !currentRaidRewardVouchers(native).IsZero() {
							t.Fatal("native source invalid", err)
						}
						card, cmd, err := freezeCurrentAntonAutomaticCard(f, owner, 3, catalog, privateLukeFiendwarRealTokenRoll(t, catalog, f, draw))
						if err != nil {
							t.Fatal(err)
						}
						amount := uint32(4000)
						if mode == 3 {
							amount = 5000
						}
						want := map[uint32]uint32{2681917: amount}
						for _, row := range native.Rewards {
							want[row.Definition.ItemID] += row.Count
						}
						privateRaidAssertContents(t, card.Plan.Rewards, want)
						if len(want) < 2 || cmd.Fingerprint != card.Plan.Fingerprint {
							t.Fatal("missing native reward or fingerprint")
						}
						items := currentAntonPartyDisplayItems(card.Plan)
						if len(items) != len(card.Plan.Rewards) {
							t.Fatal("display omitted an entitlement")
						}
						if _, err := raid.BuildRewardListBody(3, []raid.RewardDisplayCard{{CharacterID: owner.CharacterID, CardType: 1, Items: items}}); err != nil {
							t.Fatal("combined wire not representable", err)
						}
					})
				}
				nativeStart += choice.Weight
			}
		}
	}
}

// Exercise actual 659/660 manual entry and unattended advancement. Both must
// consume the same team plan and grant two pools, including wallet + native
// materials, once. These are server/wire fixtures, not native client UI tests.
func TestRealPVFPrivateLukeFiendwarManualAutomaticTwoPools(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	ctx := context.Background()
	for _, phase := range []string{"luke_phase1", "fiendwar"} {
		for _, manual := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/manual%t", phase, manual), func(t *testing.T) {
				f, sessions := r195MovieStageFixture(t, phase)
				warmAntonRewardQuestFixture(t, f)
				ensureRaidRewardAccount(t, f, "account-20")
				flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
				if flow.Single || (flow.Mode == 2 && flow.Phase != 1) || (flow.Mode == 3 && flow.Phase != 0) {
					t.Fatal("wrong final flow")
				}
				beforeWallet := map[string]int64{}
				beforeItems := map[uint16]map[uint32]int64{}
				for _, owner := range flow.Recipients {
					a, _, err := f.repos.Account.Load(ctx, owner.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					beforeWallet[owner.AccountID] = cerashop.WalletBalance(a).HappyTokenCera
					card, _, err := freezeCurrentAntonAutomaticCard(flow, owner, 3, catalog, privateLukeFiendwarRealTokenRoll(t, catalog, flow, 0))
					if err != nil {
						t.Fatal(err)
					}
					beforeItems[owner.CharacterID] = map[uint32]int64{}
					for _, row := range card.Plan.Rewards {
						if row.Definition.ItemID != 2681917 {
							beforeItems[owner.CharacterID][row.Definition.ItemID] = raidRewardItemCount(t, f.repos, strconv.Itoa(int(owner.CharacterID)), row.Definition.ItemID)
						}
					}
				}
				for _, session := range sessions {
					f.conn = session.conn.(*bufferConn)
					dispatchAntonCardRequest(t, f, session, 659, []byte{1})
					if manual {
						dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
					}
				}
				at := flow.AutoSelectAt
				for _, deadline := range flow.PartySelectAfter {
					if deadline.After(at) {
						at = deadline
					}
				}
				if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
					t.Fatal(errs)
				}
				if manual && !flow.PartyManual {
					t.Fatal("manual handler was not exercised")
				}
				for _, owner := range flow.Recipients {
					party, squad := flow.Party[owner.CharacterID], flow.Squad[owner.CharacterID]
					if party == nil || !party.Committed || squad == nil || !squad.Committed {
						t.Fatal("both pools did not commit")
					}
					a, _, err := f.repos.Account.Load(ctx, owner.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					amount := int64(4000)
					if flow.Mode == 3 {
						amount = 5000
					}
					if cerashop.WalletBalance(a).HappyTokenCera-beforeWallet[owner.AccountID] != amount {
						t.Fatal("wrong wallet delta")
					}
					for id, before := range beforeItems[owner.CharacterID] {
						var expected int64
						for _, card := range []*currentAntonRewardCard{party, squad} {
							for _, row := range card.Plan.Rewards {
								if row.Definition.ItemID == id {
									expected += int64(row.Count)
								}
							}
						}
						if got := raidRewardItemCount(t, f.repos, strconv.Itoa(int(owner.CharacterID)), id) - before; got != expected {
							t.Fatalf("native item %d delta=%d want=%d", id, got, expected)
						}
					}
				}
				for _, session := range sessions {
					// Warm inventory projection metadata before comparing replay assets.
					if err := f.service.sendSelectedCurrentContainerListsRefresh(session, "private_token_native_fixture"); err != nil {
						t.Fatal(err)
					}
					conn := session.conn.(*bufferConn)
					conn.write.Reset()
					if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
						t.Fatal(err)
					}
					wire := r195ReadFiendRewardWire(t, conn.write.Bytes())
					teamRows := 0
					for _, owner := range flow.Recipients {
						for _, row := range flow.Squad[owner.CharacterID].Plan.Rewards {
							key := r195FiendRewardDescriptor{kind: 3, owner: owner.CharacterID, id: row.Definition.ItemID, quantity: uint16(row.Count)}
							if wire.items[key] != 1 {
								t.Fatalf("team wire lost/duplicated token or native row: %+v count=%d", key, wire.items[key])
							}
							teamRows++
						}
					}
					gotTeamRows := 0
					for row, count := range wire.items {
						if row.kind == 3 {
							gotTeamRows += count
						}
					}
					if gotTeamRows != teamRows {
						t.Fatal("unexpected team wire items")
					}
				}
				frozen := cloneCurrentAntonRewardFlow(flow)
				before := r195PublicRewardAssetSnapshot(t, f.service, sessions[0])
				f.conn = sessions[0].conn.(*bufferConn)
				dispatchAntonCardRequest(t, f, sessions[0], 660, []byte{1, 3})
				if _, errs := f.service.advanceCurrentAntonRewardFlows(at.Add(time.Second), func(int64) (int64, error) { t.Fatal("settled flow rerolled"); return 0, nil }); len(errs) != 0 {
					t.Fatal(errs)
				}
				if !reflect.DeepEqual(before, r195PublicRewardAssetSnapshot(t, f.service, sessions[0])) || !reflect.DeepEqual(frozen.Squad, flow.Squad) {
					t.Fatal("replay changed entitlement or assets")
				}
			})
		}
	}
}
