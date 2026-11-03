package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func epicShopLevelFixture(t *testing.T, levels []int) (*Service, *gameSession, *bufferConn, *runtimeDungeonState) {
	t.Helper()
	s, p, c, runtime := epicShopFixture(t, 100)
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	pairs := "[piece list]"
	source[dungeonDropEquipmentList] = ""
	for i, level := range levels {
		id := 5001 + i
		pairs += fmt.Sprintf(" %d %d", id, 10300001+i)
		source[dungeonDropEquipmentList] += fmt.Sprintf("%d `shop/%d.equ`\n", id, id)
		body := "[equipment type] `[coat]`\n[rarity] 4\n[grade] 9\n"
		if level > 0 {
			body += fmt.Sprintf("[minimum level] %d\n", level)
		}
		source[fmt.Sprintf("equipment/shop/%d.equ", id)] = body
	}
	source[currentEpicPieceInfoPath] = pairs + "\n[/piece list]"
	g, _ := s.repositoryGroup()
	a, _, err := g.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range levels {
		a.Metadata[repo.AccountEpicPieceMetadataKey(uint32(10300001+i))] = "20"
	}
	if err := g.Account.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return s, p, c, runtime
}

func TestEpicShopLevel90SelectionRefreshAndDebitMatch(t *testing.T) {
	// Low/unknown/higher levels surround six valid choices; the target is 90.
	levels := []int{90, 80, 84, 85, 86, 87, 88, 89, 90, 91, 0}
	s, p, c, runtime := epicShopLevelFixture(t, levels)
	killEpicShopRoom(t, s, p, runtime)
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	checkQuote := func() {
		t.Helper()
		bodies := shopPackets(t, s, c, 745)
		if len(bodies) == 0 {
			t.Fatal("missing material quote")
		}
		body := bodies[len(bodies)-1]
		seen := map[uint32]bool{10300001: true}
		for i, id := range runtime.epicShop.Materials {
			index := int(id) - 10300001
			if index < 0 || index >= len(levels) || levels[index] < 85 || levels[index] > 90 || seen[id] {
				t.Fatalf("Lv90 material outside Lv85-90 or duplicate: %d quote=%v", id, runtime.epicShop.Materials)
			}
			seen[id] = true
			if binary.LittleEndian.Uint32(body[1+8*i:]) != id || binary.LittleEndian.Uint32(body[5+8*i:]) != 10 {
				t.Fatal("wire quote differs")
			}
		}
	}
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	checkQuote()
	first := runtime.epicShop.Materials
	s.dungeonChoice = func(limit int) (int, error) { return limit - 1, nil }
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	if first != runtime.epicShop.Materials {
		t.Fatal("replay changed materials")
	}
	if err := s.handleCurrentEpicShop(p, 817, nil); err != nil {
		t.Fatal(err)
	}
	checkQuote()
	if first == runtime.epicShop.Materials {
		t.Fatal("paid refresh did not use new random choice")
	}
	g, _ := s.repositoryGroup()
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
	a, _, _ := g.Account.Load(context.Background(), "account-1")
	for i := range levels {
		id, want := uint32(10300001+i), uint32(20)
		if i == 0 {
			want += 5
		}
		for _, material := range runtime.epicShop.Materials {
			if id == material {
				want -= 10
			}
		}
		if got := repo.ParseAccountEpicPieceBalance(a.Metadata[repo.AccountEpicPieceMetadataKey(id)]); got != want {
			t.Fatalf("piece=%d got=%d want=%d", id, got, want)
		}
	}
}

func TestEpicShopUnknownTargetAndInsufficientLevelPoolDoNotMutate(t *testing.T) {
	for _, levels := range [][]int{{90, 85, 86, 87, 88, 80, 84}, {0, 85, 86, 87, 88, 89, 90}} {
		t.Run(fmt.Sprint(levels), func(t *testing.T) {
			s, p, c, runtime := epicShopLevelFixture(t, levels)
			killEpicShopRoom(t, s, p, runtime)
			g, _ := s.repositoryGroup()
			a, _, _ := g.Account.Load(context.Background(), "account-1")
			ch, _, _ := g.Character.Load(context.Background(), "99")
			c.write.Reset()
			for _, op := range []uint16{816, 817, 818} {
				var body []byte
				if op == 816 {
					body = shopChoice(10300001)
				}
				if err := s.handleCurrentEpicShop(p, op, body); err != nil {
					t.Fatal(err)
				}
				ack := shopPackets(t, s, c, op)
				if len(ack) != 1 || ack[0][0] != 0 {
					t.Fatalf("op=%d accepted bad pool: %v", op, ack)
				}
			}
			after, _, _ := g.Account.Load(context.Background(), "account-1")
			afterCh, _, _ := g.Character.Load(context.Background(), "99")
			if !reflect.DeepEqual(a, after) || !reflect.DeepEqual(ch, afterCh) || runtime.epicShop.Target != 0 || len(shopPackets(t, s, c, 745)) != 0 {
				t.Fatal("invalid choice mutated assets/quote")
			}
		})
	}
}

func TestEpicShopUsesEquipmentMinimumLevelNotPieceGrade(t *testing.T) {
	s, p, _, runtime := epicShopLevelFixture(t, []int{90, 85, 86, 87, 88, 89, 80})
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	for key, body := range source {
		if strings.HasPrefix(key, "equipment/shop/") {
			source[key] = strings.Replace(body, "[grade] 9", "[grade] 1", 1)
		}
	}
	killEpicShopRoom(t, s, p, runtime)
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	if runtime.epicShop.Target != 10300001 {
		t.Fatal("valid minimum levels rejected by grade")
	}
	for _, id := range runtime.epicShop.Materials {
		if id == 10300007 || id == 10300001 {
			t.Fatal("wrong level/target admitted")
		}
	}
}
