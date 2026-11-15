package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var (
	errCurrentNPCShopContextUnavailable = errors.New("dnf current NPC shop context is unavailable")
	errCurrentNPCShopSaleContext        = errors.New("dnf current NPC shop sale NPC or price is unavailable")
)

// A repaired client context is useful only while the same bound character
// remains in the ready area that actually contains the catalog's NPC.
type currentNPCShopContextProof struct {
	identity boundGameSessionCharacter
	town     byte
	area     byte
}

func (s *Service) currentNPCShopContextCurrent(proof *currentNPCShopContextProof) bool {
	if proof == nil {
		return true
	}
	if !s.boundGameSessionCharacterCurrent(proof.identity) {
		return false
	}
	g := proof.identity.session
	g.townMu.Lock()
	p, ready := g.townPositionSnapshot, g.townSceneReadyCharacterID
	g.townMu.Unlock()
	return p.PositionValid && p.CharacterID == proof.identity.character && ready == proof.identity.character &&
		p.TownID == proof.town && p.AreaID == proof.area && !currentDungeonSceneActive(g) &&
		s.boundGameSessionCharacterCurrent(proof.identity)
}

func (s *Service) currentNPCShopSceneProof(g *gameSession, shop *currentNPCShopCatalog, npc uint32) (*currentNPCShopContextProof, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(g)
	if !bound || shop == nil || shop.source == nil {
		return nil, errCurrentNPCShopContextUnavailable
	}
	g.townMu.Lock()
	p := g.townPositionSnapshot
	g.townMu.Unlock()
	proof := &currentNPCShopContextProof{identity: identity, town: p.TownID, area: p.AreaID}
	if !s.currentNPCShopContextCurrent(proof) {
		return nil, errCurrentNPCShopContextUnavailable
	}
	area, found := s.townArea(int64(p.TownID), int64(p.AreaID))
	if !found {
		return nil, errCurrentNPCShopContextUnavailable
	}
	placement, err := worldmap.LoadTownPlacementMap(shop.source, area.MapPath)
	if err != nil {
		return nil, errCurrentNPCShopContextUnavailable
	}
	for _, placed := range placement.NPCs {
		if placed.NPCID == int64(npc) && s.currentNPCShopContextCurrent(proof) {
			return proof, nil
		}
	}
	return nil, errCurrentNPCShopContextUnavailable
}

// Keep exact shelf/product authorization. Repair a stale actor only from the
// server's current town placement, never by treating an arbitrary actor as a
// wildcard. Existing actor=0 and matching-actor requests retain their behavior.
func (s *Service) resolveCurrentNPCShopBuyContext(g *gameSession, shop *currentNPCShopCatalog, q currentNPCShopBuyRequest) (currentNPCShopBuyRequest, error) {
	if err := validateCurrentNPCShopBuyShelf(shop, q); err == nil {
		return q, nil
	}
	if shop == nil {
		return q, errCurrentNPCShopContextUnavailable
	}
	if shelf, known := shop.shopItems[q.ShopContext]; known {
		if _, listed := shelf[q.ItemID]; !listed {
			return q, errCurrentNPCShopProductUnavailable
		}
		npc := shop.shopNPCs[q.ShopContext]
		if npc == 0 {
			return q, errCurrentNPCShopContextUnavailable
		}
		// This dynamic NPC is not a static map placement. Its existing purchase
		// lock still verifies the exact goods, raid, area, identity and expiry
		// before the asset owner is entered.
		if q.ShopContext == currentFiendWarMerchantShop && npc == currentFiendWarMerchantNPC {
			q.ActorContext = npc
			return q, nil
		}
		proof, err := s.currentNPCShopSceneProof(g, shop, npc)
		if err != nil {
			return q, err
		}
		q.ActorContext, q.contextProof = npc, proof
		return q, nil
	}
	// Trading-system requests have no itemshop number. An unknown nonzero
	// shelf must not be reinterpreted as a trading shortcut.
	if q.ShopContext != 0 {
		return q, errCurrentNPCShopProductUnavailable
	}
	var chosen uint32
	var proof *currentNPCShopContextProof
	for npc, goods := range shop.tradingProducts {
		if _, listed := goods[q.ItemID]; !listed {
			continue
		}
		candidate, err := s.currentNPCShopSceneProof(g, shop, npc)
		if err != nil {
			continue
		}
		if proof != nil {
			return q, errCurrentNPCShopContextUnavailable
		}
		chosen, proof = npc, candidate
	}
	if proof == nil {
		return q, errCurrentNPCShopContextUnavailable
	}
	q.ActorContext, q.contextProof = chosen, proof
	return q, nil
}

// A restricted sale must use an allowed NPC and a positive resolved price.
// In particular, time stones have a trading NPC price but no ordinary value;
// a missing/stale NPC must never turn them into a successful zero-gold sale.
func validateCurrentNPCShopSaleContext(shop *currentNPCShopCatalog, p currentNPCShopPricing, actor uint32) error {
	doc, err := parseDungeonCardPVFDocument(shop.source, p.Definition.PVFPath)
	if err != nil {
		return err
	}
	tokens, restricted := doc.Section("sale npc index")
	if !restricted {
		return nil
	}
	allowed := false
	for _, token := range tokens {
		if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > math.MaxUint32 {
			return errCurrentNPCShopSaleContext
		}
		allowed = allowed || uint32(token.Int) == actor
	}
	if !allowed || p.SellGold <= 0 {
		return errCurrentNPCShopSaleContext
	}
	return nil
}

type currentNPCShopDiagnosticState struct {
	mu         sync.Mutex
	window     time.Time
	count      int
	suppressed uint64
}

// At most four small diagnostic records per two seconds and connection. The
// next emitted record accounts for suppressed records; raw errors/payloads are
// deliberately absent even when the optional packet logger is disabled.
func (s *Service) logCurrentNPCShopDiagnostic(g *gameSession, opcode uint16, stage string, q currentNPCShopBuyRequest, effective uint32, reason string) {
	if s.logger == nil || g == nil {
		return
	}
	now := s.gameplayNow()
	d := &g.npcShopDiagnostics
	d.mu.Lock()
	if d.window.IsZero() || now.Sub(d.window) >= 2*time.Second || now.Before(d.window) {
		d.window, d.count = now, 0
	}
	if d.count >= 4 {
		d.suppressed++
		d.mu.Unlock()
		return
	}
	d.count++
	suppressed := d.suppressed
	d.suppressed = 0
	d.mu.Unlock()
	s.mu.Lock()
	conn, char := g.connID, g.selectedCharacterID
	s.mu.Unlock()
	s.logger.Info("npc-shop-diagnostic", "conn_id", conn, "char_id", char, "opcode", opcode,
		"stage", stage, "reason", reason, "item_id", q.ItemID, "count", q.Count,
		"shop_context", q.ShopContext, "actor_context", q.ActorContext, "effective_actor", effective,
		"suppressed", suppressed)
}

func currentNPCShopContextError() error {
	return fmt.Errorf("%w: ready town or character changed", errCurrentNPCShopContextUnavailable)
}

func currentNPCShopRejectionReason(err error) string {
	switch {
	case errors.Is(err, errCurrentNPCShopContextUnavailable):
		return "context_changed"
	case errors.Is(err, errCurrentNPCShopSaleContext):
		return "sale_npc_or_price_unavailable"
	case errors.Is(err, errCurrentNPCShopMaterialInsufficient):
		return "materials_insufficient"
	case errors.Is(err, errCurrentNPCShopGoldInsufficient):
		return "gold_insufficient"
	case errors.Is(err, dnfnpcshop.ErrPurchaseLimit):
		return "purchase_limit"
	case errors.Is(err, errCurrentNPCShopItemLocked):
		return "item_locked"
	default:
		return "unavailable"
	}
}
