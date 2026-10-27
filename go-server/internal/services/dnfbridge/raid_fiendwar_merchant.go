package dnfbridge

import "fmt"

const currentFiendWarMerchantNPC uint32 = 20433
const currentFiendWarMerchantShop uint32 = 400000000

// 商品先按真实PVF解析；仅这个动态NPC在事务期间持raid锁，避免到期/终局和购买交错。
// 锁序与开团扣票相同：raid -> 角色资产事务。不会在资产事务中反向获取raid锁。
func (s *Service) lockCurrentFiendWarMerchantPurchase(p *gameSession, shop *currentNPCShopCatalog, q currentNPCShopBuyRequest) (func(), error) {
	goods := shop.npcItems[currentFiendWarMerchantNPC]
	_, merchantProduct := goods[q.ItemID]
	requested := q.ActorContext == currentFiendWarMerchantNPC || q.ShopContext == currentFiendWarMerchantShop
	if !requested {
		if merchantProduct {
			exclusive := true
			for npc, items := range shop.npcItems {
				if npc != currentFiendWarMerchantNPC {
					if _, ok := items[q.ItemID]; ok {
						exclusive = false
						break
					}
				}
			}
			if exclusive {
				return nil, fmt.Errorf("超时空商人专属商品缺少NPC上下文")
			}
		}
		return func() {}, nil
	}
	if !merchantProduct || !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) {
		return nil, fmt.Errorf("超时空商人商品或身份不符")
	}
	p.dungeon.mu.Lock()
	inside := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	p.townMu.Lock()
	position := p.townPositionSnapshot
	ready := p.townSceneReadyCharacterID == p.selectedCharacterID
	p.townMu.Unlock()
	if inside || !ready || !position.PositionValid {
		return nil, fmt.Errorf("超时空商人只能在已确认的城镇区域交易")
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(p.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil {
		s.raidMu.Unlock()
		return nil, fmt.Errorf("超时空商人攻坚实例不存在")
	}
	b := state.Battle
	f := b.FiendWar
	if f == nil || b.ChannelID != p.channel.ID || b.ChannelType != p.channel.Type || b.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) || b.TimedOut || b.PhaseCompleted || f.Complete || f.Failed || f.MerchantArea == 0 || !s.gameplayNow().Before(f.MerchantExpiresAt) || int(position.TownID) != f.Config.WaitingTown || int(position.AreaID) != f.MerchantArea || f.MemberArea(p.selectedCharacterID, b.MemberGroups[p.selectedCharacterID]) != f.MerchantArea {
		s.raidMu.Unlock()
		return nil, fmt.Errorf("超时空商人已离开、区域或本场归属不符")
	}
	return s.raidMu.Unlock, nil
}
