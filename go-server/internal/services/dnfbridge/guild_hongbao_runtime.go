package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentGuildHongbaoOwner() (*guild.HongbaoOwner, error) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, guild.ErrRepositoryUnavailable
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	s.guildDungeonCatalogMu.Lock()
	catalog := s.guildHongbaoCatalog
	if catalog == nil {
		catalog, err = guild.LoadHongbaoCatalog(items.source)
		if err == nil {
			s.guildHongbaoCatalog = catalog
		}
	}
	s.guildDungeonCatalogMu.Unlock()
	if err != nil {
		return nil, err
	}
	coin, err := currentDungeonCardItemReward(newCurrentDungeonDropLCG(1), items, uint32(guild.ContributionCoinID), s.gameplayNow())
	if err != nil {
		return nil, err
	}
	if !coin.Stackable || !coin.ExpireAt.IsZero() {
		return nil, guild.ErrHongbaoCatalog
	}
	return guild.NewHongbaoOwner(repos, guild.HongbaoContract{
		Catalog: catalog,
		GrantItem: func(ctx context.Context, tx r.Group, actor *r.CharacterRecord, inventory *r.InventoryRecord, product guild.HongbaoProduct, expires time.Time) (string, error) {
			if actor == nil || inventory == nil || inventory.CharacterID != actor.CharacterID {
				return "", guild.ErrHongbaoItem
			}
			definition, err := items.ResolveItem(uint32(product.ItemID))
			if err != nil {
				return "", err
			}
			if definition.UsablePeriodDays != 10 || definition.StackLimit != 1 {
				return "", guild.ErrHongbaoCatalog
			}
			now := expires.Add(-10 * 24 * time.Hour)
			item, err := currentDungeonCardItemReward(newCurrentDungeonDropLCG(1), items, uint32(product.ItemID), now)
			if err != nil {
				return "", err
			}
			if !item.Stackable || !item.ExpireAt.Equal(expires) {
				return "", guild.ErrHongbaoCatalog
			}
			if err := r.BindInventoryContainerState(ctx, repos.Settings, inventory); err != nil {
				return "", err
			}
			if inventory.Slots == nil {
				inventory.Slots = map[string]r.ItemStack{}
			}
			item.Extra["source"] = "guild_hongbao_purchase"
			reward := dungeon.CardItemReward{Stack: r.ItemStack{ItemID: item.ItemID, Count: 1, Bind: item.Bind, RawEntry: append([]byte(nil), item.RawEntry...), Extra: cloneDungeonCardStringMap(item.Extra)}, Stackable: true, SlotStart: item.SlotStart, SlotEnd: item.SlotEnd, StackLimit: item.StackLimit, ExpireAt: expires}
			slot, err := dungeon.AddResolvedRewardToInventory(inventory, reward, func(stack r.ItemStack, deadline time.Time) (r.ItemStack, error) {
				stack, _ = applyCurrentStackableExpirationAt(stack, deadline, now)
				return stack, nil
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("0:%d", slot), nil
		},
		DeliverMail: func(ctx context.Context, tx r.Group, actor r.CharacterRecord, award guild.HongbaoMailAward, now time.Time) ([]string, error) {
			var ids []string
			if award.Coins > 0 {
				id, err := r.AppendSystemMail(ctx, tx.Mailbox, r.SystemMailDelivery{
					RecipientCharacterID: actor.CharacterID, RecipientName: actor.Name, Source: "guild_hongbao",
					Title: "公会红包贡献币", Body: "公会红包已结算，贡献币请在15天内领取。",
					CompletionKey: fmt.Sprintf("hongbao:%d:coins", award.RecordID), CreatedAt: now, ExpireAt: now.Add(15 * 24 * time.Hour),
					Attachments: []r.MailAttachment{{ItemID: guild.ContributionCoinID, Count: int64(award.Coins), Bind: coin.Bind, Extra: cloneDungeonCardStringMap(coin.Extra)}},
				})
				if err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
			if award.RefundTokenCera > 0 {
				id, err := r.AppendSystemMail(ctx, tx.Mailbox, r.SystemMailDelivery{
					RecipientCharacterID: actor.CharacterID, RecipientName: actor.Name, Source: "guild_hongbao",
					Title: "公会红包未领金额返还", Body: "未领取的公会红包金额已返还为代币券，本邮件无期限。",
					CompletionKey: fmt.Sprintf("hongbao:%d:refund", award.RecordID), CreatedAt: now,
					Attachments: []r.MailAttachment{{ItemID: cerashop.TokenCeraVoucherItemID, Count: int64(award.RefundTokenCera)}},
				})
				if err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
			return ids, nil
		},
	})
}

// Background settlement recovers completed and expired records for offline senders.
// It is separate from roster/list queries and joins Service shutdown.
func (s *Service) startCurrentGuildHongbaoSettlement(ctx context.Context) {
	if !s.options.guildHongbaoEnabled {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if err := s.settleCurrentGuildHongbaos(ctx); err != nil && ctx.Err() == nil {
				s.logWarn("公会红包结算待重试", "error", err)
			}
			timer.Reset(5 * time.Second)
		}
	}()
}

func (s *Service) settleCurrentGuildHongbaos(ctx context.Context) error {
	owner, err := s.currentGuildHongbaoOwner()
	if err != nil {
		return err
	}
	now := s.gameplayNow()
	ids, err := owner.Pending(ctx, now, 64)
	if err != nil {
		return err
	}
	var first error
	for _, id := range ids {
		result, err := owner.Settle(ctx, id, now)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if !result.Replayed {
			s.projectCurrentGuildHongbaoSettlement(ctx, result)
		}
	}
	return first
}

func (s *Service) projectCurrentGuildHongbaoSettlement(ctx context.Context, result guild.HongbaoSettlementResult) {
	id, err := strconv.ParseUint(result.Sender.CharacterID, 10, 16)
	if err != nil {
		return
	}
	identity, ok := s.onlineGameSessionCharacterSnapshot(uint16(id))
	if !ok || s.accountIDForSession(identity.session) != result.Sender.AccountID {
		return
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return
	}
	defer release()
	if !s.boundGameSessionCharacterCurrent(identity) {
		return
	}
	if len(result.MailIDs) > 0 {
		if err := s.sendMailboxAlarmForSession(identity.session, identity.character, "guild_hongbao"); err != nil {
			s.logGameEvent(identity.session, "game-guild-hongbao-mail-refresh-deferred", "error", err)
		}
	}
	if result.Points > 0 {
		if err := s.sendCurrentGuildHongbaoPointsSnapshot(identity.session); err != nil {
			s.logGameEvent(identity.session, "game-guild-hongbao-points-refresh-deferred", "error", err)
		}
	}
}

func (s *Service) sendCurrentGuildHongbaoPointsSnapshot(session *gameSession) error {
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil || g == nil {
		return err
	}
	body, err := buildCurrentGuildHongbaoPointsBody(guild.SortedMembers(g))
	if err != nil {
		return err
	}
	return s.sendGameUpperSuccess(session, currentGuildHongbaoPointsOpcode, body)
}
