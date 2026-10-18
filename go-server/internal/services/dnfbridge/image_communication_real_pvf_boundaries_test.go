package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSeekerImageCommunicationRealPVFBoundaries(t *testing.T) {
	a, catalog, _ := seekerImageCommunicationFixture(t)
	for _, name := range []string{"active3741", "only-completed3734", "missing-town-quest3697", "wrong-area", "no-active", "insufficient"} {
		t.Run(name, func(t *testing.T) {
			s, se, r, conn := newCarryGoldFixture(t, 20000, 0)
			s.initialEquipmentArchive = a
			s.questCatalog = catalog
			se.connID = "real-" + name
			se.loginPacketSeen = true
			s.bindGameSessionCharacter(se, 17)
			area := byte(1)
			if name == "wrong-area" {
				area = 0
			}
			setCurrentTownPositionSceneLocked(se, 17, 14, area)
			se.townSceneReadyCharacterID = 17
			c, _, _ := r.Character.Load(context.Background(), "17")
			c.Stats["town_id"], c.Stats["area_id"] = 14, int64(area)
			q := dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{3697: {Status: "completed"}, 3734: {Status: "active"}}}
			switch name {
			case "active3741":
				delete(q.States, 3734)
				q.States[3741] = dnfrepo.QuestState{Status: "active"}
			case "only-completed3734":
				q.States[3734] = dnfrepo.QuestState{Status: "completed"}
			case "missing-town-quest3697":
				delete(q.States, 3697)
			case "no-active":
				delete(q.States, 3734)
			case "insufficient":
				c.Stats["gold"] = 9999
			}
			if err := r.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if err := r.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameCommand(se, 1, 475, nil); err != nil {
				t.Fatal(err)
			}
			after, _, _ := r.Character.Load(context.Background(), "17")
			if name == "active3741" {
				if len(assertImageCommunicationSuccess(t, conn.write.Bytes(), 10000)) != 0 || after.Stats["gold"] != 10000 {
					t.Fatal("real3741 debit/reply")
				}
			} else {
				p, _ := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
				code := byte(2)
				if name == "insufficient" {
					code = 10
				}
				if !reflect.DeepEqual(p.Body, []byte{0, code}) || !reflect.DeepEqual(after, c) {
					t.Fatalf("refusal=%x assets=%+v", p.Body, after)
				}
			}
			afterQuest, _, _ := r.Quest.Load(context.Background(), "17")
			if !reflect.DeepEqual(afterQuest, q) {
				t.Fatal("475 changed quest")
			}
		})
	}
}
