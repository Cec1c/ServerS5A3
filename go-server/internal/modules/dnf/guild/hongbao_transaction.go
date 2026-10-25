package guild

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/itemlock"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type HongbaoOwner struct {
	repos    r.Group
	uow      r.GuildSettlementUnitOfWork
	contract HongbaoContract
}

func NewHongbaoOwner(repos r.Group, c HongbaoContract) (*HongbaoOwner, error) {
	uow, ok := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork)
	if !ok || uow == nil || repos.Settings == nil || repos.Account == nil || repos.Character == nil || repos.Inventory == nil || repos.Mailbox == nil {
		return nil, ErrRepositoryUnavailable
	}
	if c.Catalog == nil || len(c.Catalog.products) != 4 || c.GrantItem == nil || c.DeliverMail == nil {
		return nil, ErrHongbaoRequest
	}
	if c.RandIntn == nil {
		c.RandIntn = func(n uint32) (uint32, error) {
			v, err := rand.Int(rand.Reader, new(big.Int).SetUint64(uint64(n)))
			if err != nil {
				return 0, err
			}
			return uint32(v.Uint64()), nil
		}
	}
	return &HongbaoOwner{repos, uow, c}, nil
}

type hongbaoReceipt struct {
	Version     int
	Owner       r.GuildOwner
	GuildID     uint32
	Fingerprint string
	Result      json.RawMessage
	At          time.Time
}
type hongbaoTx struct {
	repos     r.Group
	actor     r.CharacterRecord
	account   r.AccountRecord
	inventory r.InventoryRecord
	reg       Registry
}

// The operation key does not contain the body. Reusing an ID with another body
// must conflict, rather than silently acquiring a second receipt key.
func (o *HongbaoOwner) apply(ctx context.Context, q HongbaoRequest, kind string, body any, now time.Time, result any, fn func(*hongbaoTx) error) (bool, error) {
	if o == nil || o.uow == nil || !validHongbaoOwner(q.Owner) || q.GuildID == 0 || q.Revalidate == nil || strings.TrimSpace(q.OperationID) != q.OperationID || q.OperationID == "" || len(q.OperationID) > 256 || now.IsZero() {
		return false, ErrHongbaoRequest
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return false, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
	key := fmt.Sprintf("hongbao:v1:%s:%x", kind, sha256.Sum256([]byte(q.OperationID)))
	scope := r.GuildSettlementReceiptScope(q.Owner, key)
	replayed := false
	err = o.uow.WithinGuildSettlement(ctx, q.Owner, key, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != q.Owner.CharacterID || actor.AccountID != q.Owner.AccountID || actor.Stats["delete_flag"] != 0 {
			return ErrCharacterNotFound
		}
		old, exists, err := tx.Settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		if exists {
			var receipt hongbaoReceipt
			if json.Unmarshal([]byte(old.Values["hongbao"]), &receipt) != nil || receipt.Version != 1 || receipt.At.IsZero() || len(receipt.Fingerprint) != 64 || len(receipt.Result) == 0 {
				return ErrHongbaoState
			}
			if receipt.Owner != q.Owner || receipt.GuildID != q.GuildID || receipt.Fingerprint != fingerprint {
				return ErrHongbaoIdentity
			}
			if err := json.Unmarshal(receipt.Result, result); err != nil {
				return ErrHongbaoState
			}
			if !q.Revalidate() {
				return ErrCharacterNotFound
			}
			replayed = true
			return nil
		}
		state := &hongbaoTx{repos: tx, actor: r.CloneCharacter(actor)}
		state.account, found, err = tx.Account.Load(ctx, q.Owner.AccountID)
		if err != nil {
			return err
		}
		if !found || state.account.AccountID != q.Owner.AccountID {
			return ErrCharacterNotFound
		}
		state.account = r.CloneAccount(state.account)
		state.inventory, found, err = tx.Inventory.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			state.inventory = r.InventoryRecord{CharacterID: q.Owner.CharacterID}
		}
		if state.inventory.CharacterID != q.Owner.CharacterID {
			return ErrCharacterNotFound
		}
		state.inventory = r.CloneInventory(state.inventory)
		state.reg, err = Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := state.reg.GuildForOwner(q.Owner)
		if !found || g.ID != q.GuildID {
			return ErrNotMember
		}
		if member.JoinedAt.IsZero() || member.JoinedAt.After(now) {
			return ErrHongbaoState
		}
		if err := fn(state); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		state.actor.UpdatedAt = now
		state.account.UpdatedAt = now
		state.inventory.UpdatedAt = now
		if err := r.SaveCharacterFields(ctx, tx.Character, state.actor, r.CharacterFieldStats); err != nil {
			return err
		}
		if err := tx.Account.Save(ctx, state.account); err != nil {
			return err
		}
		if err := r.SaveInventoryFields(ctx, tx.Inventory, state.inventory, r.InventoryFieldSlots); err != nil {
			return err
		}
		if err := Save(ctx, tx.Settings, state.reg, now); err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		encoded, err = json.Marshal(hongbaoReceipt{1, q.Owner, q.GuildID, fingerprint, encoded, now})
		if err != nil {
			return err
		}
		if err := tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"hongbao": string(encoded)}, UpdatedAt: now}); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	return replayed, err
}

const hongbaoPurchaseDay = "guild_hongbao_purchase_day_end"
const hongbaoPurchaseCount = "guild_hongbao_purchase_amount"
const hongbaoContributionDay = "guild_hongbao_contribution_day_end"
const hongbaoContributionCount = "guild_hongbao_contribution_points"
const hongbaoClaimDay = "guild_hongbao_claim_day_end"
const hongbaoClaimCount = "guild_hongbao_claim_count"

func hongbaoDayValue(end, count int64, now time.Time, limit uint32) (uint32, error) {
	current := itemexpiration.NextDailyDeleteAt(now).Unix()
	if end < 0 || end > current || count < 0 || uint64(count) > uint64(limit) || (end == 0 && count != 0) {
		return 0, ErrHongbaoState
	}
	if end == current {
		return uint32(count), nil
	}
	return 0, nil
}
func hongbaoAccountCount(account r.AccountRecord, now time.Time, limit uint32) (uint32, error) {
	vals := []int64{0, 0}
	for i, key := range []string{hongbaoClaimDay, hongbaoClaimCount} {
		if raw := account.Metadata[key]; raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, ErrHongbaoState
			}
			vals[i] = v
		}
	}
	return hongbaoDayValue(vals[0], vals[1], now, limit)
}

func (o *HongbaoOwner) Purchase(ctx context.Context, q HongbaoPurchaseRequest, now time.Time) (HongbaoPurchaseResult, error) {
	var out HongbaoPurchaseResult
	if o == nil {
		return out, ErrRepositoryUnavailable
	}
	replay, err := o.apply(ctx, q.HongbaoRequest, "purchase", q.CommodityID, now, &out, func(tx *hongbaoTx) error {
		p, ok := o.contract.Catalog.ProductByCommodity(q.CommodityID)
		if !ok {
			return ErrHongbaoCatalog
		}
		g, _, _ := tx.reg.GuildForOwner(q.Owner)
		if uint32(len(g.Members)) < o.contract.Catalog.minMembers {
			return ErrHongbaoMemberCount
		}
		total, err := hongbaoDayValue(tx.actor.Stats[hongbaoPurchaseDay], tx.actor.Stats[hongbaoPurchaseCount], now, o.contract.Catalog.purchaseLimit)
		if err != nil {
			return err
		}
		if p.Price > o.contract.Catalog.purchaseLimit-total {
			return ErrHongbaoPurchaseLimit
		}
		payment, err := cerashop.PlanWalletPayment(cerashop.WalletBalance(tx.account), int64(p.Price), cerashop.PayPolicyCeraOnly)
		if err != nil {
			return err
		}
		expires := now.Add(10 * 24 * time.Hour)
		before := r.CloneInventory(tx.inventory)
		slot, err := o.contract.GrantItem(ctx, tx.repos, &tx.actor, &tx.inventory, p, expires)
		if err != nil {
			return err
		}
		granted, exists := tx.inventory.Slots[slot]
		if !exists || hongbaoSlot(slot) < 0 || granted.ItemID != p.ItemID || !granted.ExpireAt.Equal(expires) || !hongbaoItemAvailable(granted, now) {
			return ErrHongbaoItem
		}
		if old, exists := before.Slots[slot]; exists && old.ItemID > 0 && old.Count > 0 {
			return ErrHongbaoItem
		}
		// The projection is a single grant, not a second inventory owner.
		after := r.CloneInventory(tx.inventory)
		if old, exists := before.Slots[slot]; exists {
			after.Slots[slot] = old
		} else {
			delete(after.Slots, slot)
		}
		if len(before.Slots) == 0 && len(after.Slots) == 0 {
			after.Slots = before.Slots
		}
		if !reflect.DeepEqual(before.Slots, after.Slots) || !reflect.DeepEqual(before.Warehouse, after.Warehouse) {
			return ErrHongbaoItem
		}
		out = HongbaoPurchaseResult{Product: p, Slot: slot, ExpireAt: expires, Wallet: cerashop.ApplyWalletPayment(&tx.account, payment)}
		if tx.actor.Stats == nil {
			tx.actor.Stats = map[string]int64{}
		}
		tx.actor.Stats[hongbaoPurchaseDay] = itemexpiration.NextDailyDeleteAt(now).Unix()
		tx.actor.Stats[hongbaoPurchaseCount] = int64(total + p.Price)
		return nil
	})
	if err != nil {
		return HongbaoPurchaseResult{}, err
	}
	if out.Product.CommodityID != q.CommodityID || out.Slot == "" || out.ExpireAt.IsZero() {
		return HongbaoPurchaseResult{}, ErrHongbaoState
	}
	out.Replayed = replay
	return out, nil
}

func hongbaoSlot(key string) int {
	list, slot, ok := strings.Cut(key, ":")
	if !ok || list != "0" {
		return -1
	}
	n, err := strconv.ParseInt(slot, 10, 16)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != slot || r.IsAccountSharedInventorySlot(0, int16(n)) {
		return -1
	}
	return int(n)
}
func hongbaoItemAvailable(stack r.ItemStack, now time.Time) bool {
	if stack.Count != 1 || itemlock.IsLockedAt(stack, now) {
		return false
	}
	expiry := stack.ExpireAt
	for _, key := range []string{"expire_time", "expire_unix"} {
		if raw := stack.Extra[key]; raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v <= 0 {
				return false
			}
			t := time.Unix(v, 0)
			if !expiry.IsZero() && expiry.Unix() != v {
				return false
			}
			expiry = t
		}
	}
	return !expiry.IsZero() && now.Before(expiry)
}
func (o *HongbaoOwner) Issue(ctx context.Context, q HongbaoIssueRequest, now time.Time) (HongbaoIssueResult, error) {
	var out HongbaoIssueResult
	if o == nil {
		return out, ErrRepositoryUnavailable
	}
	if q.Portions < 5 || q.Portions > 30 {
		return out, ErrHongbaoRequest
	}
	replay, err := o.apply(ctx, q.HongbaoRequest, "issue", struct {
		Item     int64
		Portions uint8
	}{q.ItemID, q.Portions}, now, &out, func(tx *hongbaoTx) error {
		p, ok := o.contract.Catalog.ProductByItem(q.ItemID)
		if !ok {
			return ErrHongbaoCatalog
		}
		if tx.reg.Hongbao == nil {
			tx.reg.Hongbao = &HongbaoState{NextID: 1, Records: map[uint32]HongbaoRecord{}}
		}
		s := tx.reg.Hongbao
		if s.Records == nil {
			s.Records = map[uint32]HongbaoRecord{}
		}
		count := 0
		for _, h := range s.Records {
			if h.GuildID == q.GuildID && h.visible(now) {
				count++
			}
		}
		if count >= HongbaoVisibleLimit {
			return ErrHongbaoListFull
		}
		if s.NextID == math.MaxUint32 {
			return ErrHongbaoState
		}
		slot := ""
		index := math.MaxInt
		for key, stack := range tx.inventory.Slots {
			n := hongbaoSlot(key)
			if n >= 0 && n < index && stack.ItemID == q.ItemID && hongbaoItemAvailable(stack, now) {
				slot, index = key, n
			}
		}
		if slot == "" {
			return ErrHongbaoItem
		}
		shares, err := o.split(p.Price, q.Portions)
		if err != nil {
			return err
		}
		_, m, _ := tx.reg.GuildForOwner(q.Owner)
		h := HongbaoRecord{ID: s.NextID, GuildID: q.GuildID, Sender: q.Owner, SenderName: tx.actor.Name, JoinedAt: m.JoinedAt, Product: p, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Shares: shares}
		s.NextID++
		s.Records[h.ID] = h
		delete(tx.inventory.Slots, slot)
		out = HongbaoIssueResult{RecordID: h.ID, ItemID: q.ItemID, Portions: q.Portions, ConsumedSlot: slot}
		return nil
	})
	if err != nil {
		return HongbaoIssueResult{}, err
	}
	if out.RecordID == 0 || out.ItemID != q.ItemID || out.Portions != q.Portions || out.ConsumedSlot == "" {
		return HongbaoIssueResult{}, ErrHongbaoState
	}
	out.Replayed = replay
	return out, nil
}
func (o *HongbaoOwner) split(total uint32, count uint8) ([]uint32, error) {
	shares := make([]uint32, 0, count)
	remaining := total
	for n := uint32(count); n > 1; n-- {
		upper := uint32(min(uint64(remaining)*2/uint64(n), uint64(remaining-n+1)))
		v, err := o.contract.RandIntn(upper)
		if err != nil {
			return nil, err
		}
		if v >= upper {
			return nil, ErrHongbaoState
		}
		v++
		shares = append(shares, v)
		remaining -= v
	}
	return append(shares, remaining), nil
}

func (o *HongbaoOwner) Claim(ctx context.Context, q HongbaoClaimRequest, now time.Time) (HongbaoClaimResult, error) {
	var out HongbaoClaimResult
	if o == nil {
		return out, ErrRepositoryUnavailable
	}
	if q.RecordID == 0 {
		return out, ErrHongbaoRequest
	}
	replay, err := o.apply(ctx, q.HongbaoRequest, "claim", q.RecordID, now, &out, func(tx *hongbaoTx) error {
		if tx.reg.Hongbao == nil {
			return ErrHongbaoUnavailable
		}
		h, ok := tx.reg.Hongbao.Records[q.RecordID]
		if !ok || h.GuildID != q.GuildID {
			return ErrHongbaoUnavailable
		}
		for _, c := range h.Claims {
			if c.Owner.AccountID == q.Owner.AccountID {
				return ErrHongbaoAlreadyClaimed
			}
		}
		if h.ready(now) || h.Settlement != nil || now.Before(h.CreatedAt) {
			return ErrHongbaoUnavailable
		}
		if len(h.Claims) > 0 && now.Before(h.Claims[len(h.Claims)-1].At) {
			return ErrHongbaoState
		}
		count, err := hongbaoAccountCount(tx.account, now, o.contract.Catalog.claimLimit)
		if err != nil {
			return err
		}
		if count >= o.contract.Catalog.claimLimit {
			return ErrHongbaoClaimLimit
		}
		if tx.actor.Name == "" || tx.actor.Level <= 0 || tx.actor.Level > math.MaxUint8 {
			return ErrCharacterNotFound
		}
		amount := h.Shares[len(h.Claims)]
		wallet := cerashop.WalletBalance(tx.account)
		if wallet.HappyTokenCera > math.MaxInt32-int64(amount) {
			return ErrHongbaoState
		}
		cerashop.SetHappyTokenCeraBalance(&tx.account, wallet.HappyTokenCera+int64(amount))
		tx.account.Metadata[hongbaoClaimDay] = strconv.FormatInt(itemexpiration.NextDailyDeleteAt(now).Unix(), 10)
		tx.account.Metadata[hongbaoClaimCount] = strconv.FormatUint(uint64(count+1), 10)
		h.Claims = append(h.Claims, HongbaoClaimEntry{Owner: q.Owner, Name: tx.actor.Name, Level: uint8(tx.actor.Level), Amount: amount, At: now})
		tx.reg.Hongbao.Records[h.ID] = h
		out = HongbaoClaimResult{RecordID: h.ID, Amount: amount, Wallet: cerashop.WalletBalance(tx.account)}
		return nil
	})
	if err != nil {
		return HongbaoClaimResult{}, err
	}
	if out.RecordID != q.RecordID || out.Amount == 0 {
		return HongbaoClaimResult{}, ErrHongbaoState
	}
	out.Replayed = replay
	return out, nil
}

func (o *HongbaoOwner) Settle(ctx context.Context, id uint32, now time.Time) (HongbaoSettlementResult, error) {
	var out HongbaoSettlementResult
	if o == nil || id == 0 || now.IsZero() {
		return out, ErrHongbaoRequest
	}
	reg, err := Load(ctx, o.repos.Settings)
	if err != nil {
		return out, err
	}
	if reg.Hongbao == nil {
		return out, ErrHongbaoUnavailable
	}
	original, ok := reg.Hongbao.Records[id]
	if !ok {
		return out, ErrHongbaoUnavailable
	}
	key := fmt.Sprintf("hongbao:v1:settle:%d", id)
	err = o.uow.WithinGuildSettlement(ctx, original.Sender, key, func(tx r.Group) error {
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		if reg.Hongbao == nil {
			return ErrHongbaoState
		}
		h, ok := reg.Hongbao.Records[id]
		if !ok || h.Sender != original.Sender {
			return ErrHongbaoState
		}
		if h.Settlement != nil {
			out = *h.Settlement
			out.MailIDs = append([]string(nil), out.MailIDs...)
			out.Replayed = true
			return nil
		}
		if !h.ready(now) {
			return ErrHongbaoNotReady
		}
		actor, found, err := tx.Character.Load(ctx, h.Sender.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.AccountID != h.Sender.AccountID || actor.CharacterID != h.Sender.CharacterID {
			return ErrCharacterNotFound
		}
		actor = r.CloneCharacter(actor)
		used, err := hongbaoDayValue(actor.Stats[hongbaoContributionDay], actor.Stats[hongbaoContributionCount], now, HongbaoContributionDailyLimit)
		if err != nil {
			return err
		}
		coins := uint32(uint64(h.Product.Price-h.RemainingAmount()) * uint64(h.Product.ContributionRate) / 10000)
		coins = min(coins, HongbaoContributionDailyLimit-used)
		out = HongbaoSettlementResult{Sender: h.Sender, RecordID: id, Coins: coins}
		g, m, belongs := reg.GuildForOwner(h.Sender)
		if belongs && g.ID == h.GuildID && m.JoinedAt.Equal(h.JoinedAt) {
			if m.ContributionPoints > math.MaxUint32-coins {
				return ErrMileageOverflow
			}
			m.ContributionPoints += coins
			g.Members[h.Sender.CharacterID] = m
			g.UpdatedAt = now
			out.Points = coins
			out.RefundTokenCera = h.RemainingAmount()
		}
		if coins != 0 || out.RefundTokenCera != 0 {
			beforeMail, _, err := tx.Mailbox.Load(ctx, h.Sender.CharacterID)
			if err != nil {
				return err
			}
			ids, err := o.contract.DeliverMail(ctx, tx, actor, HongbaoMailAward{id, coins, out.RefundTokenCera}, now)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return ErrHongbaoState
			}
			mailbox, found, err := tx.Mailbox.Load(ctx, h.Sender.CharacterID)
			if err != nil {
				return err
			}
			if !found {
				return ErrHongbaoState
			}
			seen := map[string]bool{}
			for _, mid := range ids {
				mail, ok := mailbox.Mails[mid]
				_, existed := beforeMail.Mails[mid]
				if !ok || existed || seen[mid] || mail.MailID != mid || mail.RecipientCharacterID != actor.CharacterID || mail.Deleted || mail.Claimed || len(mail.Attachments) == 0 {
					return ErrHongbaoState
				}
				seen[mid] = true
			}
			out.MailIDs = append([]string(nil), ids...)
		}
		if actor.Stats == nil {
			actor.Stats = map[string]int64{}
		}
		actor.Stats[hongbaoContributionDay] = itemexpiration.NextDailyDeleteAt(now).Unix()
		actor.Stats[hongbaoContributionCount] = int64(used + coins)
		actor.UpdatedAt = now
		if err := r.SaveCharacterFields(ctx, tx.Character, actor, r.CharacterFieldStats); err != nil {
			return err
		}
		h.Settlement = &out
		h.SettledAt = now
		reg.Hongbao.Records[id] = h
		return Save(ctx, tx.Settings, reg, now)
	})
	if err != nil {
		return HongbaoSettlementResult{}, err
	}
	return out, nil
}

func (o *HongbaoOwner) Pending(ctx context.Context, now time.Time, limit int) ([]uint32, error) {
	if o == nil || now.IsZero() || limit < 1 || limit > HongbaoVisibleLimit {
		return nil, ErrHongbaoRequest
	}
	reg, err := Load(ctx, o.repos.Settings)
	if err != nil {
		return nil, err
	}
	var ids []uint32
	if reg.Hongbao != nil {
		for id, h := range reg.Hongbao.Records {
			if h.Settlement == nil && h.ready(now) {
				ids = append(ids, id)
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}
