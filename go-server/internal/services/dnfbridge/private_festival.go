package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/festival"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func parsePrivateFestivalCatalog(source pvf.Source, products map[uint32]currentCeraShopProduct) (*festival.Catalog, error) {
	text, e := source.ReadText(festival.PVFPath)
	if e != nil {
		return nil, nil
	} // optional on upstream PVFs
	d, e := pvf.Parse(festival.PVFPath, text)
	if e != nil {
		return nil, e
	}
	c, e := festival.Parse(d)
	if e != nil || c == nil {
		return c, e
	}
	for id := range c.Commodities {
		p, ok := products[id]
		if !ok || p.Count != 1 || p.CeraPrice == 0 {
			return nil, fmt.Errorf("invalid festival paid commodity %d", id)
		}
	}
	return c, nil
}
func festivalCartUnits(c *pvfCeraShopCatalog, q currentCeraShopPurchaseRequest) map[uint32]uint32 {
	out := map[uint32]uint32{}
	if c == nil || c.festivals == nil {
		return out
	}
	for _, r := range q.Items {
		if p := c.festivals.Commodities[r.CommodityID]; p != 0 {
			out[p]++
		}
	}
	return out
}
func (s *Service) preparePrivateFestivalPurchase(session *gameSession, c *pvfCeraShopCatalog, q *currentCeraShopPurchaseRequest, body []byte, seqs []*uint64) error {
	if len(festivalCartUnits(c, *q)) == 0 {
		return nil
	}
	if session == nil || session.loginRevoked.Load() || session.selectedCharacterID == 0 || q.PaymentMode != currentCeraShopPaymentModeCera || len(seqs) == 0 || seqs[0] == nil {
		return festival.ErrInvalid
	}
	serial := *seqs[0]
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return festival.ErrInvalid
	}
	q.FestivalOwner = identity
	q.FestivalAccount = s.accountIDForSession(session)
	if q.FestivalAccount == "" {
		return festival.ErrInvalid
	}
	high, ok := session.guild.purchaseSequence.currentHigh()
	if !ok || high != serial {
		return festival.ErrReplay
	}
	id, e := session.guild.purchaseSequence.identity(serial)
	if e != nil {
		return e
	}
	h := sha256.Sum256(body)
	q.FestivalRequestID = id
	q.FestivalDigest = hex.EncodeToString(h[:])
	q.FestivalSerial = serial
	return nil
}
func (s *Service) privateFestivalPreflight(session *gameSession, a *repo.AccountRecord, q currentCeraShopPurchaseRequest) error {
	if q.FestivalRequestID == "" {
		return nil
	}
	if session == nil || a == nil || session.loginRevoked.Load() {
		return festival.ErrInvalid
	}
	if q.FestivalOwner.session != session || !s.boundGameSessionCharacterCurrent(q.FestivalOwner) || q.FestivalAccount != a.AccountID || q.FestivalAccount != s.accountIDForSession(session) {
		return festival.ErrInvalid
	}
	s.mu.Lock()
	last := session.privateFestivalCommittedSerial
	s.mu.Unlock()
	if q.FestivalSerial <= last {
		return festival.ErrReplay
	}
	st, e := festival.Load(a.Metadata[festival.MetadataKey])
	if e != nil {
		return e
	}
	return festival.CheckReceipt(st, q.FestivalRequestID, q.FestivalDigest)
}
func projectPrivateFestivalPurchase(c *pvfCeraShopCatalog, a *repo.AccountRecord, character string, q currentCeraShopPurchaseRequest, purchases []currentCeraShopResolvedPurchase) error {
	units := festivalCartUnits(c, q)
	if len(units) == 0 {
		return nil
	}
	if q.FestivalRequestID == "" || len(purchases) != len(q.Items) {
		return festival.ErrInvalid
	}
	// Resolved checkout products must agree with the whitelist and paid units.
	for _, p := range purchases {
		if c.festivals.Commodities[p.Product.CommodityID] != 0 && (p.Product.Count != 1 || p.Product.CeraPrice == 0) {
			return festival.ErrInvalid
		}
	}
	st, e := festival.Load(a.Metadata[festival.MetadataKey])
	if e != nil {
		return e
	}
	if e = festival.Apply(c.festivals, &st, units, character, q.FestivalRequestID, q.FestivalDigest); e != nil {
		return e
	}
	raw, e := festival.Save(st)
	if e != nil {
		return e
	}
	if a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	a.Metadata[festival.MetadataKey] = raw
	return nil
}

// A failure here does not roll back an already-paid order: its durable outbox
// remains pending. The next authenticated status request retries it. Each mail
// and removal of its pending award commit together; no independent receipt write.
func (s *Service) settlePrivateFestival(ctx context.Context, session *gameSession) (festival.State, *festival.Catalog, error) {
	if session == nil || session.loginRevoked.Load() || session.selectedCharacterID == 0 {
		return festival.State{}, nil, festival.ErrInvalid
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return festival.State{}, nil, festival.ErrInvalid
	}
	account := s.accountIDForSession(session)
	character := strconv.Itoa(int(identity.character))
	c, e := s.currentCeraShopCatalog()
	if e != nil {
		return festival.State{}, nil, e
	}
	if c.festivals == nil {
		return festival.State{}, nil, festival.ErrInvalid
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return festival.State{}, nil, festival.ErrInvalid
	}
	uow, ok := g.MailboxAssets.(repo.AccountMailboxUnitOfWork)
	if !ok {
		return festival.State{}, nil, festival.ErrInvalid
	}
	now := s.gameplayNow()
	var state festival.State
	e = uow.WithinAccountMailbox(ctx, account, character, func(accounts repo.AccountRepository, boxes repo.MailboxRepository) error {
		if !s.boundGameSessionCharacterCurrent(identity) || session.loginRevoked.Load() {
			return festival.ErrInvalid
		}
		a, found, e := accounts.Load(ctx, account)
		if e != nil {
			return e
		}
		if !found {
			return festival.ErrInvalid
		}
		state, e = festival.Load(a.Metadata[festival.MetadataKey])
		if e != nil {
			return e
		}
		keep := make([]festival.Award, 0, len(state.Pending))
		delivered := 0
		for _, award := range state.Pending {
			if award.Character != character {
				keep = append(keep, award)
				continue
			}
			def, e := c.items.ResolveItem(award.Reward.Item)
			if e != nil {
				return e
			}
			def, e = currentPVFItemDefinitionForGrantAt(def, now)
			if e != nil {
				return e
			}
			if def.Kind != dungeonDropItemStackable || (!def.ExpirationDate.IsZero() && !def.ExpirationDate.After(now)) {
				return festival.ErrInvalid
			}
			name := c.festivals.Periods[award.Period].Name
			if name == "" {
				name = "节日礼包"
			}
			_, e = repo.AppendSystemMail(ctx, boxes, repo.SystemMailDelivery{RecipientCharacterID: character, Title: fmt.Sprintf("%s第%d套赠礼", name, award.At), Body: "本账号累计购买奖励，请查收附件。", Source: "private_festival", CompletionKey: festival.CompletionKey(award), CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: int64(award.Reward.Item), Count: int64(award.Reward.Count), ExpireAt: def.ExpirationDate}}})
			if e != nil {
				return e
			}
			delivered++
		}
		if delivered == 0 {
			return nil
		}
		state.Pending = keep
		raw, e := festival.Save(state)
		if e != nil {
			return e
		}
		if !s.boundGameSessionCharacterCurrent(identity) || session.loginRevoked.Load() {
			return festival.ErrInvalid
		}
		return repo.SaveAccountMetadataEntry(ctx, accounts, a, festival.MetadataKey, raw, now)
	})
	return state, c.festivals, e
}
