package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guardianshield"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagShieldStoreTestDeck(ch *repo.CharacterRecord, deck guardianshield.Deck) {
	for i, id := range deck {
		ch.Stats[guardianshield.DeckStatKey(i)] = int64(id)
	}
}

func TestTagShieldEmptyDeckDoesNotInventWarehouseOrAffectOtherBranches(t *testing.T) {
	for _, grow := range []int64{0x21, 0x22, 0x23, 0x24} {
		s, p, g, roster := tagCharacterProjectionFixture(t)
		roster[1].Job, roster[1].Stats["grow_type"] = "12", grow
		if err := g.Character.Save(context.Background(), roster[1]); err != nil {
			t.Fatal(err)
		}
		rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
		if err != nil {
			t.Fatal(err)
		}
		if (rows[1].shield != nil) != (grow == 0x21 || grow == 0x22) {
			t.Fatal("盾栏职业范围被扩大", grow)
		}
		a, err := freshCurrentTagTournamentActor(rows[1])
		if err != nil || a.shieldDeck != ([5]uint32{}) {
			t.Fatal(a.shieldDeck, err)
		}
		actual, _, _ := g.Character.Load(context.Background(), "60")
		if !reflect.DeepEqual(actual, roster[1]) {
			t.Fatal("预检生成盾栏")
		}
	}
}

func TestRealPVFTagShieldUsesOwnUnlockedDeckAndRejectsInconsistentSources(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	ctx := context.Background()
	s, p, g := tagPlayFixture(t)
	s.options.pvfPath = path
	resolver, err := s.newCurrentGuardianShieldResolver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.initialEquipmentArchive.ReadText(currentGuardianShieldCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentGuardianShieldCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	var questID, commonID uint32
	var ids []int
	for id := range catalog[1] {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	var quest int64
	for _, id := range ids {
		r, e := resolver(ctx, uint32(id), 1)
		if e != nil {
			t.Fatal(e)
		}
		if r.UnlockKind == alignedcmd.GuardianShieldUnlockQuest {
			questID, quest = uint32(id), r.ClearQuestID
			break
		}
	}
	ids = nil
	for id := range catalog[0] {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		r, e := resolver(ctx, uint32(id), 1)
		if e != nil {
			t.Fatal(e)
		}
		if r.UnlockKind == alignedcmd.GuardianShieldUnlockLevel && r.MinimumLevel <= 90 {
			commonID = uint32(id)
			break
		}
	}
	if questID == 0 || commonID == 0 {
		t.Fatal("现用盾栏目录缺少测试所需的任务/等级盾")
	}
	ch, _, _ := g.Character.Load(ctx, "60")
	ch.Job, ch.Stats["grow_type"] = "12", 0x21
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "60", States: map[int64]repo.QuestState{quest: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := guardianshield.NewOwner(g, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Move(ctx, guardianshield.MoveCommand{CharacterID: 60, AccountID: "a", SourceListType: 34, SourceShieldItemID: questID, DestinationListType: 33}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.SaveDeck(ctx, 60, "a", guardianshield.Deck{questID, commonID}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Character.Load(ctx, "60")
	equipment, _, _ := g.Equipment.Load(ctx, "60")
	quests, _, _ := g.Quest.Load(ctx, "60")
	tagPlayChallenge(t, s, p, tagtournament.Normal)
	row := p.tagTournamentBattle.prepared.characters[1]
	actor, err := freshCurrentTagTournamentActor(row)
	if err != nil || actor.shieldDeck != ([5]uint32{questID, commonID}) {
		t.Fatal("备用守护者没有独立盾栏", actor.shieldDeck, err)
	}
	body, err := buildCurrentTagTournamentActorWire(actor)
	if err != nil {
		t.Fatal(err)
	}
	// full/create正文的末尾是五格盾栏以及宠物两个u32，不是缔造者能量。
	for i, id := range actor.shieldDeck {
		if binary.LittleEndian.Uint32(body[len(body)-28+i*4:]) != id {
			t.Fatal("原生五格盾栏错位")
		}
	}
	if target := os.Getenv("DNFBRIDGE_TAG_SHIELD_FIXTURES"); target != "" {
		raw, _ := json.Marshal(map[string]any{"actor": body, "deck": actor.shieldDeck})
		if err := os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	after, _, _ := g.Character.Load(ctx, "60")
	actualEquipment, _, _ := g.Equipment.Load(ctx, "60")
	actualQuest, _, _ := g.Quest.Load(ctx, "60")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(equipment, actualEquipment) || !reflect.DeepEqual(quests, actualQuest) {
		t.Fatal("投影改写盾栏、穿戴或任务")
	}
	for _, scenario := range []string{"重复", "未知编号", "未解锁", "主盾不符", "准备后任务变化"} {
		t.Run(scenario, func(t *testing.T) {
			bad := row
			bad.character = repo.CloneCharacter(row.character)
			bad.shield = nil
			if err := g.Quest.Save(ctx, quests); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "重复":
				tagShieldStoreTestDeck(&bad.character, guardianshield.Deck{questID, questID})
			case "未知编号":
				tagShieldStoreTestDeck(&bad.character, guardianshield.Deck{questID, 0xffffffff})
			case "未解锁":
				if err := g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "60"}); err != nil {
					t.Fatal(err)
				}
			case "主盾不符":
				tagShieldStoreTestDeck(&bad.character, guardianshield.Deck{commonID})
			}
			err := s.prepareCurrentTagTournamentShield(ctx, g, &bad)
			if scenario == "准备后任务变化" {
				if err != nil {
					t.Fatal(err)
				}
				changed := repo.CloneQuest(quests)
				changed.States[quest] = repo.QuestState{Status: "accepted"}
				if err := g.Quest.Save(ctx, changed); err != nil {
					t.Fatal(err)
				}
				err = currentTagTournamentShieldSourceCurrent(ctx, g, bad)
			}
			if err == nil {
				t.Fatal("损坏或变化的盾栏仍被接受")
			}
		})
	}
}
