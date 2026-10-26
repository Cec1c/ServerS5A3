package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

// Native mouse interaction selects a visible NPC, independently of the nearby
// NPC finder used for the keyboard shortcut. These positions reproduce a
// mouse-opened merchant window outside that shortcut's 40-pixel rectangle.
func TestGuildMerchantMouseWindowOutsideKeyboardRange(t *testing.T) {
	for _, tc := range []struct {
		name            string
		shop, npc, base uint32
	}{{"mercenary", 211, 411, 389}, {"defence", 201, 421, 390}} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, repos := merchantPurchaseFixture(t)
			p.townPositionSnapshot.PositionX, p.townPositionSnapshot.PositionY = 1100, 271
			sendMerchantRequest(t, s, p, 1354, merchantPurchaseBody(10156091, 1, tc.shop, tc.npc), 60)
			if got := guildReply(t, p, 1354); !bytes.Equal(got, []byte{1}) {
				t.Fatalf("mouse purchase rejected: %x", got)
			}
			p.conn.(*bufferConn).write.Reset()
			sendMerchantRequest(t, s, p, 840, merchantUpgradeBody(tc.base, 1, 2, 0), 61)
			if got := guildReply(t, p, 840); !bytes.Equal(got, []byte{1}) {
				t.Fatalf("mouse upgrade rejected: %x", got)
			}
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil || len(reg.MerchantPurchaseReceipts) != 1 || len(reg.MerchantUpgradeReceipts) != 1 || reg.Guilds[1].Essence.AP != 98998 {
				t.Fatalf("purchase and upgrade not committed: %+v %v", reg, err)
			}
		})
	}
}

func TestGuildGuardianMouseWindowOutsideKeyboardRange(t *testing.T) {
	s, p, repos := guildGuardianFixture(t)
	p.townPositionSnapshot.PositionX, p.townPositionSnapshot.PositionY = 263, 432
	sendGuardianRequest(t, s, p, guardianRequestBody(8, 1), 60)
	if got := guildReply(t, p, 1350); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("mouse guardian purchase rejected: %x", got)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Guardians.ActiveID != 1 {
		t.Fatalf("guardian not committed: %+v %v", reg, err)
	}
}
