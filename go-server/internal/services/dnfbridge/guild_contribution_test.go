package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func contributionBridgeCatalog(t *testing.T, s *Service) {
	t.Helper()
	text := "[hongbao point reward]\n`guild dungeon clear` 5 10 `guild chat` 1 2 `guild attendance` 1 3 `party dungeon clear` 2 1\n[/hongbao point reward]"
	item := "[name]\n`公会贡献币`\n[grade]\n1\n[rarity]\n2\n[attach type]\n`[trade]`\n[minimum level]\n1\n[stackable type]\n`[quest]` 0\n"
	for key, target := range map[string]*string{"GUILD_CONTRIBUTION_EXTRACTED_ETC": &text, "GUILD_CONTRIBUTION_EXTRACTED_ITEM": &item} {
		if path := os.Getenv(key); path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			*target = string(b)
		}
	}
	cat, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "490003262 `490003001/chn_490003262.stk`", "stackable/490003001/chn_490003262.stk": item, guild.ContributionCatalogPath: text})
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	s.options.guildContributionPolicy = "daily-v1"
	if _, err := s.currentGuildContributionContract(); err != nil {
		t.Fatal(err)
	}
}

func contributionMatureBridgeMembers(t *testing.T, s *Service, repos r.Group) {
	t.Helper()
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range reg.Guilds {
		for id, m := range g.Members {
			m.JoinedAt = s.gameplayNow().Add(-48 * time.Hour)
			g.Members[id] = m
		}
	}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
}

func TestGuildContributionRealDefinitionChatAndLogin(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	contributionBridgeCatalog(t, s)
	contributionMatureBridgeMembers(t, s, repos)
	ctx := context.Background()
	for range 2 {
		if err := s.handleCurrentPeerChat(p, 17, guildChatRequest(29, "贡献测试")); err != nil {
			t.Fatal(err)
		}
	}
	// The production login event shares the same task-item projection. A
	// second login repairs the view but does not grant another three coins.
	for range 2 {
		s.grantOptionalGuildContributionAttendanceLocked(p)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "29")
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil || inv.Slots["0:177"].ItemID != guild.ContributionCoinID || inv.Slots["0:177"].Count != 5 || reg.Guilds[1].Members["29"].ContributionPoints != 5 {
		t.Fatal(inv, reg, err)
	}
	points := 0
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 1301 {
			points++
		}
	}
	if points == 0 {
		t.Fatal("missing authoritative contribution projection")
	}
}

func TestGuildContributionTaskInventoryFullMailAndInvalidChat(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	contributionBridgeCatalog(t, s)
	contributionMatureBridgeMembers(t, s, repos)
	ctx := context.Background()
	inv, _, _ := repos.Inventory.Load(ctx, "29")
	inv.CharacterID = "29"
	if inv.Slots == nil {
		inv.Slots = map[string]r.ItemStack{}
	}
	for i := 177; i <= 232; i++ {
		inv.Slots[fmt.Sprint("0:", i)] = r.ItemStack{ItemID: int64(1000 + i), Count: 1}
	}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentPeerChat(p, 17, guildChatRequest(29, "")); err != nil {
		t.Fatal(err)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "29")
	if len(box.Mails) != 0 {
		t.Fatal("invalid chat granted coins")
	}
	for range 2 {
		if err := s.handleCurrentPeerChat(p, 17, guildChatRequest(29, "邮件测试")); err != nil {
			t.Fatal(err)
		}
	}
	box, _, _ = repos.Mailbox.Load(ctx, "29")
	if len(box.Mails) != 1 {
		t.Fatal(box)
	}
	for _, m := range box.Mails {
		if m.Attachments[0].Count != 2 || m.ExpireAt.Sub(m.CreatedAt) != 15*24*time.Hour {
			t.Fatal(m)
		}
	}
}

func TestGuildContributionFrozenParticipantsSurvivePeerReturn(t *testing.T) {
	s, peers, runtime := rankingPartyFixture(t)
	s.options.guildRankingEnabled = false
	s.options.guildContributionPolicy = "daily-v1"
	if err := s.freezeCurrentGuildContributionRun(peers[0], runtime); err != nil {
		t.Fatal(err)
	}
	run := runtime.partyInstance.guildContributionRun
	if run == nil || len(run.members) != 2 || len(run.guilds) != 2 {
		t.Fatal(run)
	}
	delete(runtime.partyInstance.sessions, 2)
	delete(runtime.partyInstance.scenes, 2)
	s.runtimePartyManagerForService().Leave(2, peers[1].characterGeneration)
	if err := s.freezeCurrentGuildContributionRun(peers[0], runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.partyInstance.guildContributionRun != run || len(run.members) != 2 {
		t.Fatal("return erased terminal facts")
	}
}

func TestGuildContributionTerminalRejectsInvalidParty(t *testing.T) {
	for _, mode := range []string{"left", "wrong-party", "missing-scene", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, runtime := rankingPartyFixture(t)
			s.options.guildContributionPolicy = "daily-v1"
			switch mode {
			case "left":
				s.runtimePartyManagerForService().Leave(2, peers[1].characterGeneration)
			case "wrong-party":
				runtime.partyInstance.key.PartyID++
			case "missing-scene":
				delete(runtime.partyInstance.scenes, 2)
			case "stale":
				s.gameSessions[2] = &gameSession{selectedCharacterID: 2, characterGeneration: 999}
			}
			if err := s.freezeCurrentGuildContributionRun(peers[0], runtime); err != nil {
				t.Fatal(err)
			}
			if run := runtime.partyInstance.guildContributionRun; run == nil || len(run.members) != 0 {
				t.Fatal(run)
			}
		})
	}
}

type contributionFailSettlement struct {
	r.CharacterSettlementUnitOfWork
}

func (contributionFailSettlement) WithinGuildSettlement(context.Context, r.GuildOwner, string, func(r.Group) error) error {
	return errors.New("contribution SQL unavailable")
}

func TestGuildContributionLoginFailureRefreshRecovery(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	contributionBridgeCatalog(t, s)
	contributionMatureBridgeMembers(t, s, repos)
	original := repos.CharacterSettlement
	repos.CharacterSettlement = contributionFailSettlement{original}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	p.initialTownLegacySceneReadyAccepted = true
	if err := s.sendCurrentInitialTownGuildStateProjectionLocked(p, "contribution-test"); err != nil {
		t.Fatal("optional reward blocked entry", err)
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok || !p.initialTownGuildStateProjectionSent || p.guildContributionLoginRetry != identity {
		t.Fatal("login/retry state")
	}
	repos.CharacterSettlement = original
	for range 2 {
		guildDispatch(t, s, p, 339, nil)
	}
	if p.guildContributionLoginRetry != (boundGameSessionCharacter{}) {
		t.Fatal("refresh did not recover")
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "29")
	if inv.Slots["0:177"].Count != 3 {
		t.Fatal("recovery did not award once", inv)
	}
}
