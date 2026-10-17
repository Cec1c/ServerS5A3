package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 账号金库档位、对应商品和价格来自同一份运行PVF，包含移出旧目录的新工具。
func (c *pvfCeraShopCatalog) loadAccountCargoUpgrades() {
	c.accountCargoOnce.Do(func() {
		c.accountCargoTargets = make(map[uint32]int64)
		c.accountCargoNext = make(map[int64]currentCeraShopProduct)
		c.accountCargoItems = make(map[uint32]bool)
		if c.source == nil {
			c.accountCargoErr = errCurrentCeraShopCatalogUnavailable
			return
		}
		doc, err := parseDungeonCardPVFDocument(c.source, "etc/accountcargo.etc")
		if err != nil {
			c.accountCargoErr = err
			return
		}
		values := doc.Ints("upgrade info")
		if len(values) == 0 || len(values)%6 != 0 {
			c.accountCargoErr = fmt.Errorf("%w: 账号金库升级配置行不完整", errCurrentCeraShopProductUnavailable)
			return
		}
		previous := int64(0)
		for offset := 0; offset < len(values); offset += 6 {
			capacity, commodity := values[offset], values[offset+5]
			if capacity <= previous || capacity > 240 || (capacity != 1 && capacity%8 != 0) {
				c.accountCargoErr = fmt.Errorf("%w: 账号金库档位无效=%d", errCurrentCeraShopProductUnavailable, capacity)
				return
			}
			if commodity > 0 && commodity <= int64(^uint32(0)) {
				product, found := c.Product(uint32(commodity))
				if !found || product.Count != 1 {
					c.accountCargoErr = fmt.Errorf("%w: 账号金库升级商品缺失=%d", errCurrentCeraShopProductUnavailable, commodity)
					return
				}
				c.accountCargoTargets[product.CommodityID] = capacity
				c.accountCargoNext[previous] = product
				c.accountCargoItems[product.ItemID] = true
			}
			previous = capacity
		}
	})
}

// 仅在商城窗口仍发送旧档位时建立个人金库商品索引；不在启动时预载商城全部道具。
func (c *pvfCeraShopCatalog) nextPersonalCargoProduct(capacity uint16) (currentCeraShopProduct, uint16, bool) {
	c.personalCargoOnce.Do(func() {
		c.personalCargoProducts = make(map[uint16]currentCeraShopProduct)
		ids := make([]uint32, 0, len(c.products))
		for id := range c.products {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			product := c.products[id]
			if product.Section != "item" || product.Count != 1 {
				continue
			}
			definition, err := c.items.ResolveItem(product.ItemID)
			if err != nil {
				continue
			}
			if target, ok := c.personalCargoUpgradeTarget(definition); ok && target != 0 {
				if _, exists := c.personalCargoProducts[target]; !exists {
					c.personalCargoProducts[target] = product
				}
			}
		}
	})
	// 个人金库逐档增加16格，缺少下一档商品时拒绝，不能按旧价跳到远处档位。
	target := uint16(24)
	for target <= capacity && target <= 264 {
		target += 16
	}
	product, found := c.personalCargoProducts[target]
	return product, target, found && target <= 264
}

func resolveCurrentCeraShopCargoPurchases(ctx context.Context, repos dnfrepo.Group, catalog *pvfCeraShopCatalog, accountID, characterID string, purchases []currentCeraShopResolvedPurchase) error {
	var personalCapacity uint16
	personalLoaded := false
	for index := range purchases {
		purchase := &purchases[index]
		if purchase.PersonalCargoUpgradeTarget != 0 {
			if !personalLoaded {
				if repos.Settings == nil {
					return errCurrentCeraShopOwnerUnavailable
				}
				record, found, err := repos.Settings.Load(ctx, dnfrepo.CharacterContainerStateScope(characterID))
				if err != nil {
					return err
				}
				personalCapacity = uint16(currentExeInitialPersonalCargoSlotCount)
				if found {
					state, err := dnfrepo.CharacterContainerStateFromSettings(record, characterID)
					if err != nil {
						return err
					}
					personalCapacity = state.PersonalCargoSlotCount
				}
				personalLoaded = true
			}
			purchase.PersonalCargoUpgradeFrom = personalCapacity
			if purchase.PersonalCargoUpgradeTarget <= personalCapacity {
				product, target, found := catalog.nextPersonalCargoProduct(personalCapacity)
				if !found {
					return errCurrentCeraShopProductUnavailable
				}
				purchase.Product, purchase.PersonalCargoUpgradeTarget = product, target
			}
			personalCapacity = purchase.PersonalCargoUpgradeTarget
		}
		if purchase.AccountCargoUpgrade {
			catalog.loadAccountCargoUpgrades()
			if catalog.accountCargoErr != nil {
				return fmt.Errorf("%w: 账号金库升级配置不可用: %v", errCurrentCeraShopProductUnavailable, catalog.accountCargoErr)
			}
			if repos.Account == nil {
				return errCurrentCeraShopOwnerUnavailable
			}
			account, found, err := repos.Account.Load(ctx, accountID)
			if err != nil {
				return err
			}
			previous, parseErr := strconv.ParseInt(account.Metadata["account_cargo_level"], 10, 64)
			if !found || parseErr != nil || previous <= 0 {
				return errCurrentCeraShopProductUnavailable
			}
			product, found := catalog.accountCargoNext[previous]
			if !found {
				return errCurrentCeraShopProductUnavailable
			}
			purchase.Product = product
			purchase.AccountCargoUpgradeFrom = previous
			purchase.AccountCargoUpgradeTarget = catalog.accountCargoTargets[product.CommodityID]
		}
	}
	return nil
}

// 容量及升级通知必须先于op64，客户端才会用新容量刷新商城档位与价格。
func (s *Service) sendCurrentCeraShopCargoRefresh(ctx context.Context, session *gameSession, result currentCeraShopCommitResult) error {
	if result.PersonalCargoSlotCount != 0 {
		// 当前EXE的class0/op66处理器0x2012520，子类型1读取u16容量，
		// 只更新档位，且仅在金库界面已打开时刷新界面。购买期间发送整份
		// list2会重建物品容器；换角色后未打开过金库的路径出现过闪退。
		// 扩容不改变物品，用容量通知也能让随后的op64按新档位更新商城。
		body := make([]byte, 4)
		binary.LittleEndian.PutUint16(body, 1)
		binary.LittleEndian.PutUint16(body[2:], result.PersonalCargoSlotCount)
		if err := s.sendGameUpperRawClass(session, currentPremiumActivatedMsgID, body, 0); err != nil {
			return err
		}
		s.logGameEvent(session, "个人金库升级容量通知", "容量", result.PersonalCargoSlotCount,
			"msg_id", currentPremiumActivatedMsgID, "subtype", 1, "刷新方式", "仅容量，不重建物品列表")
	}
	for _, upgrade := range []struct {
		capacity uint16
		opcode   uint16
		list     byte
	}{
		{result.AccountCargoSelectionKey, uint16(dnfenum.CmdPacketUpgradeAccountCargo), 12},
	} {
		if upgrade.capacity == 0 {
			continue
		}
		body, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, upgrade.list)
		if !ok {
			return errCurrentCeraShopOwnerUnavailable
		}
		if upgrade.opcode != 0 {
			if err := s.sendGameUpperRawClass(session, upgrade.opcode, []byte{1}, dnfproto.DefaultChannelClassification); err != nil {
				return err
			}
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), body); err != nil {
			return err
		}
	}
	return nil
}
