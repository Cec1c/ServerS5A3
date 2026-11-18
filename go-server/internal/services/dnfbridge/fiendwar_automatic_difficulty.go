package dnfbridge

import (
	"encoding/binary"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Automatic encounters bypass the client's dungeon selector. Carry its DGN
// difficulty into the request instead of silently selecting normal (zero).
// The paired Fiendwar_Diff.tbl changes HP at difficulty2 by a factor of three.
// Read before replacing parties, so missing metadata cannot leave a temporary
// encounter roster behind. Other dungeon selection paths retain their inputs.
func (s *Service) currentFiendWarEncounterSelectBody(id uint32) ([]byte, error) {
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return nil, err
	}
	d, found := table.FindDungeon(int64(id))
	if !found {
		return nil, fmt.Errorf("超时空自动入场副本不存在: %d", id)
	}
	return currentFiendWarDeclaredSelectBody(d)
}

func currentFiendWarDeclaredSelectBody(d worldmap.Dungeon) ([]byte, error) {
	v := d.Metadata.Difficulty
	if d.ID <= 0 || d.ID > int64(^uint32(0)) || !v.Set || v.Value < 0 || v.Value > 4 {
		return nil, fmt.Errorf("超时空自动入场难度不可用: dungeon=%d difficulty=%+v", d.ID, v)
	}
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, uint32(d.ID))
	body[4] = byte(v.Value)
	return body, nil
}
