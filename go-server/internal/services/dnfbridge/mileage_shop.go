package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/mileage"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type currentMileageProduct struct {
	Item uint32
	Rule mileage.Product
}
type currentMileageCatalog struct {
	Products  map[uint32]currentMileageProduct
	CashLimit uint32
}

// ReturnMileageShop reads the first uint32 of the native 9-byte snapshot as its balance.
const currentMileageStateMsgID uint16 = 427

func parseCurrentMileageCatalog(source pvf.Source) (*currentMileageCatalog, error) {
	d, err := parseDungeonCardPVFDocument(source, "etc/mileageshop_chn.etc")
	if err != nil {
		return nil, err
	}
	c := &currentMileageCatalog{Products: map[uint32]currentMileageProduct{}}
	ts, ok := d.Section("item")
	if !ok {
		return nil, mileage.ErrInvalid
	}
	v := currentCeraShopPVFValues(ts)
	if len(v) == 0 || len(v)%9 != 0 {
		return nil, mileage.ErrInvalid
	}
	for i := 0; i < len(v); i += 9 {
		var n [9]uint32
		for _, j := range []int{0, 1, 2, 3, 4, 5, 7, 8} {
			var valid bool
			n[j], valid = currentCeraShopPVFUint32(v[i+j])
			if !valid {
				return nil, mileage.ErrInvalid
			}
		}
		// Columns7/8 are the native display/event category, not currencies.
		// The active catalog uses categories0,2,101. Prices are points only.
		if n[0] == 0 || n[1] == 0 || n[2] == 0 || n[3] != 0 || n[4] == 0 || n[5] != 0 || n[8] != 0 || (n[7] != 0 && n[7] != 2 && n[7] != 101) {
			return nil, mileage.ErrInvalid
		}
		if _, duplicate := c.Products[n[0]]; duplicate {
			return nil, mileage.ErrInvalid
		}
		c.Products[n[0]] = currentMileageProduct{Item: n[1], Rule: mileage.Product{Commodity: n[0], Price: n[4], Count: n[2]}}
	}
	if ts, ok = d.Section("purchasing limit"); ok {
		v = currentCeraShopPVFValues(ts)
		if len(v)%6 != 0 {
			return nil, mileage.ErrInvalid
		}
		for i := 0; i < len(v); i += 6 {
			id, valid := currentCeraShopPVFUint32(v[i])
			p, found := c.Products[id]
			limit, limitOK := currentCeraShopPVFUint32(v[i+1])
			if !valid || !found || !limitOK || limit == 0 || p.Rule.Limit != 0 {
				return nil, mileage.ErrInvalid
			}
			for j := 2; j < 6; j++ {
				if zero, ok := currentCeraShopPVFUint32(v[i+j]); !ok || zero != 0 {
					return nil, mileage.ErrInvalid
				}
			}
			p.Rule.Limit = limit
			c.Products[id] = p
		}
	}
	ts, ok = d.Section("limit mileage by cash")
	if !ok {
		return nil, mileage.ErrInvalid
	}
	v = currentCeraShopPVFValues(ts)
	if len(v) != 1 {
		return nil, mileage.ErrInvalid
	}
	c.CashLimit, ok = currentCeraShopPVFUint32(v[0])
	if !ok || c.CashLimit == 0 || c.CashLimit > mileage.MaximumCashEarned {
		return nil, mileage.ErrInvalid
	}
	return c, nil
}

func parseCurrentMileagePurchase(body []byte) (currentCeraShopPurchaseRequest, error) {
	if len(body) < 19 || body[0] != 1 || body[1] != 0 || body[2] == 0 || body[2] > 33 || body[3] != 0 || len(body) != 4+int(body[2])*15 {
		return currentCeraShopPurchaseRequest{}, mileage.ErrInvalid
	}
	for i := 0; i < int(body[2]); i++ {
		o := 4 + i*15
		if body[o] != 0 || body[o+7] != 0 || body[o+8] != 0 || binary.LittleEndian.Uint32(body[o+9:o+13]) != 0 {
			return currentCeraShopPurchaseRequest{}, mileage.ErrInvalid
		}
	}
	return parseCurrentCeraShopPurchaseRequest(body)
}

func buildCurrentMileageStateBody(state mileage.State) []byte {
	b := make([]byte, 9)
	binary.LittleEndian.PutUint32(b[1:5], uint32(state.Balance))
	binary.LittleEndian.PutUint32(b[5:9], uint32(state.CashEarned))
	return b
}
func (s *Service) sendCurrentMileageState(session *gameSession) error {
	if s == nil || !s.options.mileageShopEnabled || session == nil {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Account == nil {
		return mileage.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	a, found, err := repos.Account.Load(ctx, s.accountIDForSession(session))
	if err != nil {
		return err
	}
	if !found {
		return mileage.ErrInvalid
	}
	state, err := mileage.Load(a)
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	return s.sendGameUpperRawClass(session, currentMileageStateMsgID, buildCurrentMileageStateBody(state), 0)
}

func (s *Service) handleCurrentMileagePurchase(session *gameSession, body []byte, sequences []*uint64) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-mileage-purchase-rejected", "error", err)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}
	if !s.options.mileageShopEnabled || session == nil || session.loginRevoked.Load() || !session.loginPacketSeen || len(sequences) == 0 || sequences[0] == nil {
		return fail(mileage.ErrInvalid)
	}
	q, err := parseCurrentMileagePurchase(body)
	if err != nil {
		return fail(err)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return fail(mileage.ErrInvalid)
	}
	account := s.accountIDForSession(session)
	character := strconv.FormatUint(uint64(identity.character), 10)
	serial := *sequences[0]
	high, ok := session.guild.purchaseSequence.currentHigh()
	if !ok || high != serial {
		return fail(mileage.ErrReplay)
	}
	nonce, err := session.guild.purchaseSequence.identity(0)
	if err != nil {
		return fail(err)
	}
	cera, err := s.currentCeraShopCatalog()
	if err != nil {
		return fail(err)
	}
	catalog, err := parseCurrentMileageCatalog(cera.source)
	if err != nil {
		return fail(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(mileage.ErrInvalid)
	}
	owner, err := cerashop.NewOwner(repos)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		return fail(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	var products []currentMileageProduct
	var rules []mileage.Product
	for _, row := range q.Items {
		p, found := catalog.Products[row.CommodityID]
		if !found {
			return fail(mileage.ErrInvalid)
		}
		products = append(products, p)
		rules = append(rules, p.Rule)
	}
	var entries []currentItemListEntry
	now := s.gameplayNow()
	_, err = owner.Checkout(ctx, cerashop.CheckoutCommand{
		AccountID: account, CharacterID: character, SettingsScope: r.CharacterContainerStateScope(character), UpdatedAt: now,
		Preflight: func(a *r.AccountRecord) error {
			if !s.boundGameSessionCharacterCurrent(identity) || session.loginRevoked.Load() || account != s.accountIDForSession(session) {
				return mileage.ErrInvalid
			}
			if high, ok := session.guild.purchaseSequence.currentHigh(); !ok || high != serial {
				return mileage.ErrReplay
			}
			return mileage.Redeem(a, character, nonce, serial, rules)
		},
		Project: func(a *cerashop.CheckoutAssets) (cerashop.CheckoutChanges, error) {
			if a.SettingsFound {
				state, err := r.CharacterContainerStateFromSettings(*a.Settings, character)
				if err != nil {
					return cerashop.CheckoutChanges{}, err
				}
				a.Inventory.ContainerState = &state
			}
			changed := map[int]bool{}
			for _, p := range products {
				definition, err := cera.items.ResolveItem(p.Item)
				if err != nil {
					return cerashop.CheckoutChanges{}, err
				}
				definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
				if err != nil {
					return cerashop.CheckoutChanges{}, err
				}
				if definition.Kind != dungeonDropItemStackable || currentPVFItemTargetContainer(definition).ListType != 0 {
					return cerashop.CheckoutChanges{}, fmt.Errorf("unsupported mileage item container %d", p.Item)
				}
				slots, err := grantCurrentCeraShopProduct(a.Inventory, definition, p.Rule.Count)
				if err != nil {
					return cerashop.CheckoutChanges{}, err
				}
				for _, slot := range slots {
					changed[int(slot)] = true
				}
			}
			keys := make([]int, 0, len(changed))
			for slot := range changed {
				keys = append(keys, slot)
			}
			sort.Ints(keys)
			for _, slot := range keys {
				key := currentCeraShopInventorySlotKey(0, int16(slot))
				stack := a.Inventory.Slots[key]
				e := currentItemListEntryFromStack(0, int16(slot), stack)
				stack.RawEntry = append([]byte(nil), e.data[:]...)
				if stack.Extra == nil {
					stack.Extra = map[string]string{}
				}
				stack.Extra["last_grant_source"] = "mileage_shop"
				a.Inventory.Slots[key] = stack
				entries = append(entries, e)
			}
			if !s.boundGameSessionCharacterCurrent(identity) {
				return cerashop.CheckoutChanges{}, mileage.ErrInvalid
			}
			return cerashop.CheckoutChanges{Inventory: true}, nil
		},
	})
	if err != nil {
		return fail(err)
	}
	for _, p := range products {
		if err := s.sendGameUpperRawClass(session, 64, buildCurrentCeraShopPurchaseSuccessBodyWithCount(p.Rule.Commodity, p.Rule.Count), 1); err != nil {
			return err
		}
	}
	if err := s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, entries), 0); err != nil {
		return err
	}
	return s.sendCurrentMileageState(session)
}
