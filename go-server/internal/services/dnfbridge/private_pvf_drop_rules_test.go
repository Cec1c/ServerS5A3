package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func privateNativeHellText(rate, rarity int) string {
	thresholds := make([]string, 7)
	for i := range thresholds {
		if i < rarity {
			thresholds[i] = "0"
		} else {
			thresholds[i] = "1000000"
		}
	}
	return fmt.Sprintf("[drop prob count]\n1\n[dungeon difficulty drop prob]\n1 200 %d %d %d %d %d\n[basis of rarity dicision]\n2 %s %s\n[item drop ref table]\n%d 1 1\n", rate, rate, rate, rate, rate, strings.Join(thresholds, " "), strings.Join(thresholds, " "), hellPartyRewardTestBasisLevel)
}

func TestPrivateNativePVFHellChangesRealRewards(t *testing.T) {
	// Old server knobs must not override the PVF route.
	t.Setenv(hellPartyRewardRarityEnv, "epic")
	t.Setenv(abyssEpicWindowEnv, "0")
	t.Setenv(hellPartyRewardPoolEnv, "itemdictionary")
	for _, tc := range []struct {
		name                      string
		rate, rarity, rolls, want int
	}{{"zero", 0, 0, 8, 0}, {"all_common", 1000, 0, 8, 8}, {"all_epic", 1000, 4, 3, 3}, {"no_rolls", 1000, 4, 0, 0}, {"empty_rarity_bucket", 1000, 3, 8, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			source := bridgeHellPartyRewardPVF()
			source[hellPartyRewardDropInfoPath] = privateNativeHellText(tc.rate, tc.rarity)
			if tc.rarity == 4 {
				source["equipment/weapon/test_drop.equ"] = strings.Replace(source["equipment/weapon/test_drop.equ"], "[rarity]\n0", "[rarity]\n4", 1)
			}
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			request := hellPartyRewardRequest{DungeonMinimumLevel: 10, DungeonBasisLevel: hellPartyRewardTestBasisLevel, AbyssPartyDifficulty: 1, DungeonDifficulty: 1, RewardRollCount: int64(tc.rolls), dropCatalog: catalog, rng: newCurrentDungeonDropLCG(1)}
			result, err := currentNativePVFHellReward(request)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Selected) != tc.want || result.RateSource != hellPartyRewardDropInfoPath || result.EpicWindowActive {
				t.Fatalf("result=%+v", result)
			}
			if tc.want > 0 && result.RaritySelected[tc.rarity] != tc.want {
				t.Fatalf("PVF rarity ignored: %+v", result)
			}
		})
	}
}

func TestPrivateNativePVFHellActualDeathReplay(t *testing.T) {
	source := bridgeAbyssEpicFragmentPVF()
	source[hellPartyRewardDropInfoPath] = privateNativeHellText(1000, 0)
	s, session, conn, runtime, _, repos := prepareAbyssEpicFragmentRuntime(t, source)
	s.options.currentClientVersion = "official-24.9"
	t.Setenv(hellPartyRewardRollEnv, "vendor")
	first, last := hellPartyRewardActorKeys(t, runtime)
	death := func(key uint32) []abyssEpicFragmentDropRow {
		conn.write.Reset()
		body := make([]byte, 88)
		binary.LittleEndian.PutUint32(body, key)
		binary.LittleEndian.PutUint16(body[4:], 99)
		body[30] = 1
		if err := s.handleDungeonMonsterDeath(session, body); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		return abyssEpicFragmentDropRows(t, packet.Body)
	}
	var firstRows []abyssEpicFragmentDropRow
	for _, key := range first {
		firstRows = append(firstRows, death(key)...)
	}
	equipment, fragments := abyssEpicFragmentSplit(t, firstRows)
	if len(equipment) != 0 || len(fragments) != 0 {
		t.Fatalf("first group equipment=%d pieces=%d", len(equipment), len(fragments))
	}
	for _, key := range last[:len(last)-1] {
		if rows := death(key); len(rows) != 0 {
			t.Fatal("unfinished group dropped items")
		}
	}
	final := last[len(last)-1]
	rows := death(final)
	equipment, fragments = abyssEpicFragmentSplit(t, rows)
	if len(equipment) != 8 || len(fragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("last group equipment=%d fragments=%d", len(equipment), len(fragments))
	}
	before := len(runtime.DropOwner.byObjectKey)
	death(final)
	if len(runtime.DropOwner.byObjectKey) != before {
		t.Fatal("duplicate death issued new drops")
	}
	// Pickup is tested through the actual inventory handler and replay receipt.
	key := equipment[0].ObjectKey
	if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
		t.Fatal(err)
	}
	bag, _, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	count := int64(0)
	for _, v := range bag.Slots {
		if v.ItemID == int64(hellPartyRewardTestItem) {
			count += v.Count
		}
	}
	if count != 1 {
		t.Fatalf("pickup replay count=%d", count)
	}
}

func TestPrivatePVFMapDifficultyPartyAndTemplateRates(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		difficulty, party, template int
		goldWant                    bool
		templateWant                bool
	}{{"disabled_difficulty", 0, 1, 0, false, false}, {"enabled", 1, 1, 0, true, false}, {"disabled_party", 1, 0, 0, false, false}, {"template_only", 0, 1, 10000, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, _ := prepareCurrentDungeonDropTest(t, 1, 3227)
			source := s.dungeonMonsterTable.source.(bridgePVFSource)
			text := currentDungeonGoldDropTestRuleText()
			text = strings.Replace(text, "1.00 1.00 1.00 1.40 1.10", fmt.Sprintf("1 %d 1 1 1", tc.difficulty), 1)
			text = strings.Replace(text, "1 1 1 1 1 1.3", fmt.Sprintf("1 %d 1 1 1 1.3", tc.party), 1)
			text = strings.Replace(text, "[private monster template probability]\n1000", fmt.Sprintf("[private monster template probability]\n%d", tc.template), 1)
			source[dungeonCardDropRulePath] = text
			source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
			r.partyInstance = &dungeonPartyInstance{members: map[uint16]time.Time{99: time.Now(), 100: time.Now()}, nextObjectKey: r.NextObjectKey}
			wires, err := s.planCurrentDungeonMonsterDrops(r, r.Room.Snapshot().Monsters[0], currentSceneActorObjectKey(99))
			if err != nil {
				t.Fatal(err)
			}
			gold, material := false, false
			for _, w := range wires {
				switch binary.LittleEndian.Uint32(w.Item.data[2:6]) {
				case 0:
					gold = true
				case 3227:
					material = true
				}
			}
			if gold != tc.goldWant || material != tc.templateWant {
				t.Fatalf("gold=%v material=%v", gold, material)
			}
		})
	}
}

type privatePVFSourceOverride struct {
	*platformpvf.Archive
	replacements map[string]string
}

func (s privatePVFSourceOverride) ReadText(path string) (string, error) {
	if text, ok := s.replacements[strings.ToLower(path)]; ok {
		return text, nil
	}
	return s.Archive.ReadText(path)
}

func TestPrivateRealPVFDropRulesAndSpecialOverrides(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base, err := a.ReadText(hellPartyRewardDropInfoPath)
	if err != nil {
		t.Fatal(err)
	}
	w, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := w.FindDungeon(currentTimeRiftDungeonID)
	if !ok {
		t.Fatal("time rift missing")
	}
	for _, rate := range []int{0, 1000000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			start := strings.Index(base, "["+privateSpecialEpicSection+"]")
			if start < 0 {
				t.Fatal("private table missing")
			}
			text := base[:start] + fmt.Sprintf("[%s]\n7539 %d 6063 %d 6064 %d 6065 %d\n[/%s]\n[%s]\n4 1000000\n[/%s]\n", privateSpecialEpicSection, rate, rate, rate, rate, privateSpecialEpicSection, privateDemonCountSection, privateDemonCountSection)
			source := privatePVFSourceOverride{a, map[string]string{hellPartyRewardDropInfoPath: text}}
			m, err := newPVFDungeonMonsterCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{pvfPath: path, currentClientVersion: "official-24.9"}, initialEquipmentArchive: a, dungeonMonsterTable: m}
			def, err := s.prepareCurrentTimeRiftLoot(d, 2)
			if err != nil {
				t.Fatal(err)
			}
			if def.EpicRate != float64(rate)/1000000 {
				t.Fatal("time rift did not read PVF")
			}
			for _, id := range []int64{6063, 6064, 6065} {
				road, e := s.prepareCurrentEpicRoadLoot(id)
				if e != nil {
					t.Fatal(e)
				}
				if road.EpicRate != float64(rate)/1000000 {
					t.Fatal("epic road ignored PVF")
				}
				items := road.selectItems(newCurrentDungeonDropLCG(11))
				if (len(items) == 1) != (rate == 1000000) {
					t.Fatal("epic road actual draw ignored PVF")
				}
			}
			for seed := uint32(1); seed <= 20; seed++ {
				items := def.selectItems(newCurrentDungeonDropLCG(seed))
				want := def.FragmentCount
				if rate > 0 {
					want++
				}
				if len(items) != want {
					t.Fatalf("time rift drops=%d want=%d", len(items), want)
				}
			}
			config, e := loadPrivatePVFDropConfig(source)
			if e != nil {
				t.Fatal(e)
			}
			demon := currentRiftLootDefinition{Equipment: []uint32{10}, PVFEpicCounts: config.counts, MultiEpicCount: true}
			if len(demon.selectItems(newCurrentDungeonDropLCG(1))) != 4 {
				t.Fatal("demon count ignored PVF")
			}
		})
	}
	m, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	drops, err := m.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	config, err := drops.privatePVFDropConfig()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]int{7539: 200000, 6063: 150000, 6064: 200000, 6065: 250000} {
		if config.special[id] != want {
			t.Fatalf("special %d=%d", id, config.special[id])
		}
	}
	request := hellPartyRewardRequest{DungeonMinimumLevel: 87, DungeonBasisLevel: 90, AbyssPartyDifficulty: 1, DungeonDifficulty: 2, RewardRollCount: 8, dropCatalog: drops, rng: newCurrentDungeonDropLCG(1)}
	result, err := currentNativePVFHellReward(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rate != 19 || result.Rolls != 8 || result.RarityMode != "pvf" || result.EpicWindowActive {
		t.Fatalf("native real PVF=%+v", result)
	}
	t.Logf("native rate=%d rolls=%d selected=%d rarity=%v grade=%d..%d; special PVF config=%v", result.Rate, result.Rolls, len(result.Selected), result.RaritySelected, result.GradeMin, result.GradeMax, config.special)
}

func TestPrivatePVFDropConfigRejectsInvalid(t *testing.T) {
	for _, section := range []string{"[private special epic probabilities]\n7539 -1", "[private special epic probabilities]\n7539 1000001", "[private special epic probabilities]\n7539 0 7539 1", "[private demon rift epic counts]\n1 999999"} {
		source := bridgeHellPartyRewardPVF()
		source[hellPartyRewardDropInfoPath] = section
		if _, err := loadPrivatePVFDropConfig(source); err == nil {
			t.Fatalf("invalid config accepted: %s", section)
		}
	}
}
