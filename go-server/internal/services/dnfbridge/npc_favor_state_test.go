package dnfbridge

import (
	"encoding/binary"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestNPCFavorSnapshotRestoresOrdinaryCharacterAndDailyRollover(t *testing.T) {
	now := time.Date(2026, 11, 14, 5, 59, 59, 0, time.FixedZone("CST", 8*3600))
	ch := dnfrepo.CharacterRecord{Stats: map[string]int64{"npc_favor_45_points": 1533, "npc_favor_45_day": 20261113, "npc_favor_45_gifts": 2, "npc_favor_2_points": 770, "npc_favor_2_day": 20261113, "npc_favor_2_gifts": 1}}
	body, err := buildCurrentNPCFavorState(ch, now)
	if err != nil || len(body) != 21 || body[0] != 2 {
		t.Fatalf("snapshot=%x err=%v", body, err)
	}
	if binary.LittleEndian.Uint32(body[1:5]) != 2 || binary.LittleEndian.Uint32(body[5:9]) != 770 || body[9] != 1 || body[10] != 0 || binary.LittleEndian.Uint32(body[11:15]) != 45 || binary.LittleEndian.Uint32(body[15:19]) != 1533 || body[19] != 2 || body[20] != 0 {
		t.Fatalf("native row layout=%x", body)
	}
	after, err := buildCurrentNPCFavorState(ch, now.Add(time.Second))
	if err != nil || after[9] != 0 || after[19] != 0 || binary.LittleEndian.Uint32(after[15:19]) != 1533 {
		t.Fatalf("six-AM=%x err=%v", after, err)
	}
	if ch.Stats["npc_favor_45_gifts"] != 2 {
		t.Fatal("projection wrote durable state")
	}
	empty, err := buildCurrentNPCFavorState(dnfrepo.CharacterRecord{}, now)
	if err != nil || len(empty) != 1 || empty[0] != 0 {
		t.Fatalf("empty actor reset=%x %v", empty, err)
	}
}

func TestNPCFavorSnapshotRejectsNativeCountOverflow(t *testing.T) {
	ch := dnfrepo.CharacterRecord{Stats: map[string]int64{}}
	for i := 1; i <= 256; i++ {
		ch.Stats[fmt.Sprintf("npc_favor_%d_points", i)] = int64(i)
	}
	if _, err := buildCurrentNPCFavorState(ch, time.Now()); err == nil {
		t.Fatal("wrapped native count")
	}
}
