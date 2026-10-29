package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagEntrySupportsEarringSlotAndKeepsIndependentAuraEntitlement(t *testing.T) {
	var fixtures []map[string]any
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, flags := range []int64{0x10, 0x13, 0x1f} {
			for _, aura := range []int64{0, 1} {
				t.Run(fmt.Sprintf("模式%d-栏位%x-光环%d", mode, flags, aura), func(t *testing.T) {
					s, p, g := tagPlayFixture(t)
					for _, id := range []string{"29", "60"} {
						ch, _, err := g.Character.Load(context.Background(), id)
						if err != nil {
							t.Fatal(err)
						}
						ch.Stats["ex_equip_slot_stat"], ch.Stats["aura_flag"] = flags, aura
						if err := g.Character.Save(context.Background(), ch); err != nil {
							t.Fatal(err)
						}
					}
					before, _, _ := g.Character.Load(context.Background(), "29")
					backup, _, _ := g.Character.Load(context.Background(), "60")
					tagPlayChallenge(t, s, p, mode)
					want := byte(flags & 0x13)
					if aura != 0 {
						want |= 8
					}
					// tagPlayChallenge已逐字节核对1458实际1389，进图不能再重发。
					{
						body := p.tagTournamentBattle.prepared.characterBody
						nameSize := int(binary.LittleEndian.Uint32(body[5:9]))
						actor := body[9+nameSize:]
						statSize := int(binary.LittleEndian.Uint32(actor[10:14]))
						offset := 14 + statSize
						if actor[offset] != want || actor[offset+1] != 0 {
							t.Fatal("耳环/光环位丢失或被写进独立bool", actor[offset:offset+2], want)
						}
						fixtures = append(fixtures, map[string]any{"mode": mode, "stored": flags, "aura": aura, "expected": want, "actor": append([]byte(nil), actor...)})
					}
					tagPlayMoveNext(t, s, p)
					if p.tagTournamentBattle.run.Snapshot().Round != 0 {
						t.Fatal("已开耳环的队伍未能进入首回合")
					}
					after, _, _ := g.Character.Load(context.Background(), "29")
					other, _, _ := g.Character.Load(context.Background(), "60")
					if after.Stats["gold"] != before.Stats["gold"]-150000 || after.Stats["ex_equip_slot_stat"] != flags || after.Stats["aura_flag"] != aura || !reflect.DeepEqual(backup, other) {
						t.Fatal("重试重复扣费或改写了队员栏位/资产")
					}
				})
			}
		}
	}
	if target := os.Getenv("DNFBRIDGE_TAG_SLOT_NATIVE_FIXTURES"); target != "" {
		data, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
