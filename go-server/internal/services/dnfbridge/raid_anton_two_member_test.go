package dnfbridge

import (
	"context"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Runs the official online admission caller against the exact release PVF;
// repositories and authenticated sessions are explicit engineering fixtures.
func TestCurrentAntonTwoMemberReleaseAdmission(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_ANTON_TWO_MEMBER_PVF")
	if path == "" {
		t.Skip("requires two-member release PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := raid.LoadAdmissionRule(a, "etc/raid/anton.etc")
	if err != nil || rule.MinimumMembers != 2 || rule.SoloMinimumMembers != 1 || rule.MaximumMembers != 20 {
		t.Fatal("incorrect release admission", rule, err)
	}
	for _, tc := range []struct {
		name                 string
		members              int
		short, single, allow bool
	}{
		{"normal_one_rejected", 1, false, false, false},
		{"normal_two_started_once", 2, false, false, true},
		{"normal_second_short_atomic_reject", 2, true, false, false},
		{"normal_twenty_allowed", 20, false, false, true},
		{"normal_twenty_one_rejected", 21, false, false, false},
		{"single_one_preserved", 1, false, true, true},
		{"single_two_rejected", 2, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(raidStartEntryCostGuardEnv, "off") // Official must still enforce.
			f := raidSafetyFixture(t, a, tc.members)
			f.service.options.currentClientVersion = "official-24.9"
			f.state.CurrentLayout, f.state.CurrentUIFlags, f.state.TailFlag = true, [3]byte{}, 0
			if tc.single {
				f.state.CurrentUIFlags[0], f.state.TailFlag = 1, 1
			}
			f.state.NameBytes = []byte("two-member-qa")
			session := f.session()
			session.loginPacketSeen, session.accountFromToken = true, true
			f.service.bindGameSessionCharacter(session, 19)
			for i := range f.state.Members {
				m := &f.state.Members[i]
				m.GroupIndex, m.SlotOrder = byte(i/runtimeRaidGroupSize+1), byte(i%runtimeRaidGroupSize)
				if i > 0 {
					peer := f.session()
					peer.selectedCharacterID, peer.accountID = m.CharID, "account-"+strconv.Itoa(int(m.CharID))
					peer.loginPacketSeen, peer.accountFromToken = true, true
					f.service.bindGameSessionCharacter(peer, m.CharID)
				}
			}
			if tc.short {
				if err := f.repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "20", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
			}
			enterRaidStartWaitingFixture(t, f, "")
			state, ok, err := f.service.startRuntimeRaidWithEntryCost(session)
			if ok != tc.allow || f.state.Started != tc.allow {
				t.Fatal("wrong admission", ok, err, f.state.Started)
			}
			if tc.allow {
				if err != nil || state.Battle == nil || state.Battle.Single != tc.single || len(state.Battle.MemberAccounts) != tc.members {
					t.Fatal("wrong battle", state.Battle, err)
				}
				if !tc.single && (state.Battle.Anton == nil || len(state.Battle.Dungeons) != len(state.Battle.Configuration.PhaseDungeons[0])) {
					t.Fatal("normal phase lost")
				}
				if _, repeated, _ := f.service.startRuntimeRaidWithEntryCost(session); repeated {
					t.Fatal("repeat start accepted")
				}
			} else if f.state.Battle != nil {
				t.Fatal("rejected battle retained")
			}
			for i, m := range f.state.Members {
				inventory, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(m.CharID)))
				want := int64(3)
				if tc.allow {
					want--
				}
				if tc.short && i == 1 {
					want = 0
				}
				if err != nil || !found || inventory.Slots["0:50"].Count != want {
					t.Fatal("ticket transaction mismatch", m.CharID, inventory, want, err)
				}
			}
		})
	}
}
