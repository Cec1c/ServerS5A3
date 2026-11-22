package dnfbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestP0ServiceStartupArchivesExpiredGuildHistory(t *testing.T) {
	s, _, repos, now := guildPresenceFixture(t)
	ctx := context.Background()

	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	oldAt := now.Add(-48 * time.Hour)
	reqID := fmt.Sprintf("%064x", 0x999)
	reg.ContractClaims = map[string]dnfguild.ContractClaimReceipt{
		reqID: {
			Owner:   dnfrepo.GuildOwner{AccountID: "a1", CharacterID: "1"},
			GuildID: 1,
			ItemID:  490001385,
			Count:   1,
			At:      oldAt,
			DayEnd:  itemexpiration.NextDailyDeleteAt(oldAt),
			Slots:   []int16{88},
		},
	}
	if err := dnfguild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}

	if err := s.archiveExpiredGuildHistory(ctx); err != nil {
		t.Fatalf("archiveExpiredGuildHistory failed: %v", err)
	}
	after, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.ContractClaims) != 0 {
		t.Fatalf("expected startup archive to remove expired contract claim, got %d", len(after.ContractClaims))
	}
	archived, found, err := dnfguild.LoadArchivedReceipt(ctx, repos.Settings, reqID)
	if err != nil || !found || archived.Kind != dnfguild.ArchivedReceiptKindContractClaim {
		t.Fatalf("expected archived receipt after startup archive, found=%v item=%+v err=%v", found, archived, err)
	}
}
