package dnfbridge

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	"testing"
	"time"
)

func TestCurrentFiendWarBoardUsesNativeIDsWidthsAndKeepsUnknownEnemyIdentity(t *testing.T) {
	now := time.Unix(1800000000, 0)
	b := &fiendwar.FiendWarBattle{Config: &fiendwar.FiendWarBattleConfig{Areas: map[int]fiendwar.FiendWarArea{2: {Index: 2, Dungeon: -1}, 3: {Index: 3, Dungeon: 9000, LimitSeconds: 300}}, Symbols: map[string]uint32{"radar state": 81}}, Symbols: map[uint32]int{81: 0, 105: 1}, Areas: map[int]fiendwar.FiendWarAreaState{3: {Enemy: fiendwar.FiendWarUnit{ID: 1, Type: 1}, Warning: true}}, MemberAreas: map[uint16]int{11: 3}, Timers: map[fiendwar.FiendWarTimerKey]fiendwar.FiendWarTimer{{Area: 3, Kind: "move_enemy"}: {StartedAt: now, Deadline: now.Add(time.Minute)}}}
	v := currentFiendWarProjection{RaidKey: 1, Battle: b, Members: []runtimeRaidMemberState{{CharID: 11, GroupIndex: 1}}, Deadline: now.Add(time.Hour)}
	packets, err := buildCurrentFiendWarBoard(v, currentFiendWarProjection{}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	widths := map[uint16]int{1485: 2, 1486: 13, 1487: 10, 1488: 14, 1489: 10, 1491: 8}
	enemyID := uint32(0)
	seen := map[uint16]bool{}
	for _, p := range packets {
		if p.opcode >= 1452 && p.opcode <= 1460 {
			t.Fatal("使用了参考版旧编号", p.opcode)
		}
		seen[p.opcode] = true
		if width, ok := widths[p.opcode]; ok {
			if len(p.body) == 0 || len(p.body) != 1+int(p.body[0])*width {
				t.Fatalf("通知%d行宽不符: %x", p.opcode, p.body)
			}
		}
		if p.opcode == 1488 {
			enemyID = binary.LittleEndian.Uint32(p.body[2:])
			if enemyID == 0 || p.body[6] != 0 {
				t.Fatalf("未侦察敌军必须保留身份并使用问号脸: %x", p.body)
			}
		}
	}
	for _, op := range []uint16{1485, 1486, 1488, 1489, 1490, 1491, 585, 597} {
		if !seen[op] {
			t.Fatal("缺少战况表投影", op)
		}
	}
	prior := v
	prior.Battle = b.Clone()
	b.Symbols[81] = 1
	packets, err = buildCurrentFiendWarBoard(v, prior, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range packets {
		if p.opcode == 1488 {
			if p.body[0] != 1 || binary.LittleEndian.Uint32(p.body[2:]) != enemyID || p.body[6] != 1 || binary.LittleEndian.Uint32(p.body[7:]) != 1 {
				t.Fatalf("雷达揭示重复删除或换了敌军: %x", p.body)
			}
		}
	}
}
