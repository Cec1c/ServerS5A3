package dnfbridge

import (
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNativeTeamRaidPremiumDisplayFromRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	loaders := []struct {
		name string
		load func() (raid.AntonRewardConfiguration, error)
	}{
		{"anton", func() (raid.AntonRewardConfiguration, error) { return raid.LoadAntonRewardConfiguration(archive) }},
		{"luke", func() (raid.AntonRewardConfiguration, error) { return raid.LoadLukeRewardConfiguration(archive) }},
		{"fiendwar", func() (raid.AntonRewardConfiguration, error) { return raid.LoadFiendWarRewardConfiguration(archive) }},
	}
	for _, loader := range loaders {
		t.Run(loader.name, func(t *testing.T) {
			config, err := loader.load()
			if err != nil {
				t.Fatal(err)
			}
			seen := [2]bool{}
			for _, phase := range config.Phases {
				for _, choice := range phase.Choices {
					if choice.Type != "squad_item" || choice.Flags > 1 || seen[choice.Flags] {
						continue
					}
					seen[choice.Flags] = true
					plan, err := resolveCurrentAntonConfiguredReward(catalog, choice, func(int64) (int64, error) { return 0, nil }, time.Unix(1800000000, 0))
					if err != nil {
						t.Fatal(err)
					}
					before := cloneCurrentAntonRewardPlan(plan)
					items := currentAntonPartyDisplayItems(plan)
					if len(items) == 0 || !reflect.DeepEqual(before, plan) {
						t.Fatal("display changed the frozen entitlement")
					}
					body, err := raid.BuildRewardListBody(3, []raid.RewardDisplayCard{{CharacterID: 19, CardType: 1, Items: items}})
					if err != nil {
						t.Fatal(err)
					}
					for i, item := range items {
						// Native NOTI600 passes this first descriptor byte to
						// ItemReward+44; all three raid UIs require value 1
						// for the premium opening animation. The last byte
						// instead suppresses a limit-capped item.
						if item.ObjectKind != choice.Flags || body[6+8*i] != choice.Flags || item.Flags != 0 || body[13+8*i] != 0 {
							t.Errorf("pool=%d premium=%d item=%d kind=%d wire=%d limit=%d", choice.ContainerID, choice.Flags, i, item.ObjectKind, body[6+8*i], item.Flags)
						}
					}
				}
			}
			if seen != [2]bool{true, true} {
				t.Fatal("real squad configuration lacks ordinary/premium controls", seen)
			}
		})
	}
}
