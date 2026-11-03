package dnfbridge

import (
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestTimeRiftBossRewardDoesNotDependOnAddDeathOrder(t *testing.T) {
	for _, bossFirst := range []bool{false, true} {
		name := "adds_first"
		if bossFirst {
			name = "boss_first"
		}
		t.Run(name, func(t *testing.T) {
			source := bridgeDungeonMovePVF(false)
			source[bridgeDungeonMoveDungeonPath] = strings.ReplaceAll(source[bridgeDungeonMoveDungeonPath], "[tutorial dungeon]\n1\n", "")
			source[bridgeDungeonMoveNextMapPath] = strings.ReplaceAll(source[bridgeDungeonMoveNextMapPath], "`[normal]`", "`[boss]`") + "3001 10 0 200 200 0 0 0 `[fixed]` `[normal]`\n"
			s, runtime := prepareSyntheticMoveRuntimeFromPVF(t, source)
			s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			g := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			body[0] = 1
			if err := s.handleDungeonMoveMap(g, body); err != nil {
				t.Fatal(err)
			}
			scene, _ := runtime.Session.Scene()
			if !scene.Boss || scene.Cleared {
				t.Fatal("fixture must enter a live boss room")
			}
			// Supply a frozen real-shape drop catalog; the test controls only the
			// dungeon classification and uses the ordinary retirement handler.
			lootService, _, _ := prepareCurrentEpicPieceDropTest(t)
			drops, err := lootService.dungeonMonsterTable.DropCatalog()
			if err != nil {
				t.Fatal(err)
			}
			s.options.currentClientVersion = "official-24.9"
			runtime.Dungeon.ID = currentTimeRiftDungeonID
			runtime.riftLoot = &currentRiftLootDefinition{Pieces: []uint32{epicPieceBookTestPieceA}, FragmentCount: 3, DropCatalog: drops, MonsterCatalog: lootService.dungeonMonsterTable}
			var boss, add uint32
			for _, m := range runtime.Room.Snapshot().Monsters {
				isBoss, _, _ := currentDungeonRuntimeObjectBossActor(runtime, m.ObjectKey, runtimeDungeonMonsterAnnounced)
				if isBoss {
					boss = m.ObjectKey
				} else {
					add = m.ObjectKey
				}
			}
			if boss == 0 || add == 0 {
				t.Fatal("fixture needs one boss and one add")
			}
			order := []uint32{add, boss}
			if bossFirst {
				order = []uint32{boss, add}
			}
			for _, key := range append(order, boss, add) {
				death := doc94Official249DeathBody(key)
				binary.LittleEndian.PutUint16(death[4:6], currentSceneActorObjectKey(g.selectedCharacterID))
				if err := s.handleDungeonMonsterDeath(g, death); err != nil {
					t.Fatal(err)
				}
			}
			count := uint32(0)
			if runtime.DropOwner != nil {
				for _, drop := range runtime.DropOwner.byObjectKey {
					if drop.Item.ItemID == epicPieceBookTestPieceA {
						count += drop.Amount
					}
				}
			}
			if count != 3 {
				t.Fatalf("fragments=%d want3 after deaths and replay", count)
			}
		})
	}
}
