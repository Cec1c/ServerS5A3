package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"longheng.io/server/internal/modules/dnf/guild"
	"os"
	"testing"
)

func TestGuildGuardianCapturedGrowthRequests(t *testing.T) {
	s, p, repos := guildGuardianFixture(t)
	source := guardianBridgeSource(t)
	if os.Getenv("GUILD_GUARDIAN_EXTRACTED_ROOT") == "" {
		text := "\n[guardian exp]\n"
		for level := 1; level < 50; level++ {
			text += fmt.Sprintf("%d %d 0 ", level, level*18)
		}
		text += "\n[/guardian exp]\n[guardian skill]\n1 1500 0 2 15000 200 3 50000 1000 4 100000 5000\n[/guardian skill]\n"
		source["etc/agitwar.etc"] += text
	}
	var err error
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	sendGuardianRequest(t, s, p, guardianRequestBody(8, 1), 10)
	for i, raw := range []string{"02000000010000000100000002000000", "00000000010000000000000012000000"} {
		body, err := hex.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		p.conn.(*bufferConn).write.Reset()
		sendGuardianRequest(t, s, p, body, uint16(11+i))
		if got := guildReply(t, p, 1350); !bytes.Equal(got, []byte{1}) {
			t.Fatalf("captured request %d: %x", i, got)
		}
		exportEssenceBody(t, fmt.Sprintf("growth-%d-1350", i), guildReply(t, p, 1350))
		exportEssenceBody(t, fmt.Sprintf("growth-%d-1297", i), guildReply(t, p, 1297))
		// Same wire retry recovers the permanent receipt without another debit.
		p.conn.(*bufferConn).write.Reset()
		sendGuardianRequest(t, s, p, body, uint16(11+i))
		if got := guildReply(t, p, 1350); !bytes.Equal(got, []byte{1}) {
			t.Fatalf("retry %d: %x", i, got)
		}
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]
	row := g.Guardians.Owned[0]
	if row.Level != 2 || row.Skills[0] != 2 || row.APProgress != 0 || g.Essence.AP != 98482 || len(reg.GuardianReceipts) != 3 {
		t.Fatalf("growth state %+v balance %+v receipts %d", row, g.Essence, len(reg.GuardianReceipts))
	}
}
