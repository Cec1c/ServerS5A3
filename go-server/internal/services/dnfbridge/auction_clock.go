package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	auction "longheng.io/server/internal/modules/dnf/auction"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentAuctionMaintenanceTimer = "dnf:auction:escrow-maintenance"

func (s *Service) startCurrentAuctionClock(ctx context.Context) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	s.auctionMu.Lock()
	defer s.auctionMu.Unlock()
	if s.auctionMaintenanceContext != nil && s.auctionMaintenanceContext.Err() == nil {
		return
	}
	s.auctionMaintenanceContext = ctx
	s.auctionMaintenanceScheduled = false
	s.auctionMaintenanceWake = make(chan struct{}, 1)
	s.wg.Add(1)
	go s.runCurrentAuctionMaintenance(ctx, s.auctionMaintenanceWake)
	s.scheduleCurrentAuctionMaintenanceLocked(0)
}

func (s *Service) scheduleCurrentAuctionMaintenanceLocked(delay time.Duration) {
	ctx := s.auctionMaintenanceContext
	if ctx == nil || ctx.Err() != nil || s.auctionMaintenanceScheduled {
		return
	}
	wake := s.auctionMaintenanceWake
	s.auctionMaintenanceScheduled = true
	if err := s.ensureGameplayTimeQueue().ScheduleAfter(currentAuctionMaintenanceTimer, delay, func(time.Time) {
		if ctx.Err() != nil {
			return
		}
		// Shared gameplay timers must never wait on the auction mutex or SQL.
		select {
		case wake <- struct{}{}:
		default:
		}
	}); err != nil {
		s.auctionMaintenanceScheduled = false
		s.logGameEvent(nil, "拍卖结算计时登记失败", "error", err)
	}
}

func (s *Service) runCurrentAuctionMaintenance(ctx context.Context, wake <-chan struct{}) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
		s.auctionMu.Lock()
		if ctx.Err() != nil {
			s.auctionMu.Unlock()
			return
		}
		s.auctionMaintenanceScheduled = false
		operation, cancel := context.WithTimeout(ctx, createWriteTimeout)
		pending, err := s.maintainCurrentAuctionSafelyLocked(operation)
		cancel()
		if err != nil {
			s.logGameEvent(nil, "拍卖到期结算暂缓", "error", err)
		}
		if pending || err != nil {
			s.scheduleCurrentAuctionMaintenanceLocked(30 * time.Second)
		}
		s.auctionMu.Unlock()
	}
}

func (s *Service) maintainCurrentAuctionSafelyLocked(ctx context.Context) (pending bool, err error) {
	// The shared time queue used to recover callback panics. Keep the same
	// isolation after moving SQL work to the service-owned worker.
	defer func() {
		if recovered := recover(); recovered != nil {
			pending = true
			err = fmt.Errorf("auction maintenance panic: %v", recovered)
		}
	}()
	return s.maintainCurrentAuctionLocked(ctx)
}

// 重启后从持久托管恢复到期和未投递邮件。离线买卖双方不依赖打开拍卖窗口。
// 空托管不会创建系统角色，也不会继续空轮询。
func (s *Service) maintainCurrentAuctionLocked(ctx context.Context) (bool, error) {
	r, ok := s.repositoryGroup()
	if !ok || r.Inventory == nil || r.Character == nil {
		return false, nil
	}
	escrow, found, err := r.Inventory.Load(ctx, auction.EscrowCharacterID)
	if err != nil || !found || len(escrow.Slots) == 0 {
		return false, err
	}
	subjects := map[string]bool{}
	now := time.Now()
	for key, item := range escrow.Slots {
		if !currentAuctionPendingKey(key) {
			continue
		}
		if strings.HasPrefix(key, "auction:listing:") {
			if expires, err := strconv.ParseInt(item.Extra["expires_at"], 10, 64); err == nil && time.Unix(expires, 0).After(now) {
				continue // Live listings need no recipient recovery yet.
			}
		}
		for _, field := range []string{"seller_id", "buyer_id", "bidder_id"} {
			if id := strings.TrimSpace(item.Extra[field]); id != "" {
				subjects[id] = true
			}
		}
	}
	ids := make([]string, 0, len(subjects))
	for id := range subjects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Continue after the last attempted recipient, including failed recipients.
	// A bad early mailbox cannot monopolize every bounded maintenance window.
	if len(ids) > 0 {
		start := sort.SearchStrings(ids, s.auctionMaintenanceCursor)
		for start < len(ids) && ids[start] <= s.auctionMaintenanceCursor {
			start++
		}
		ids = append(ids[start:], ids[:start]...)
	}
	owner, err := s.currentAuctionOwner()
	if err != nil {
		return true, err
	}
	s.settleCurrentAuctionWonListingsWithContextLocked(ctx, nil)
	var deferred []error
	for index, id := range ids {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if index >= 8 {
			break
		}
		s.auctionMaintenanceCursor = id
		actor, present, err := r.Character.Load(ctx, id)
		if err != nil {
			deferred = append(deferred, err)
			continue
		}
		if !present || actor.AccountID == "" || actor.Stats["delete_flag"] != 0 {
			deferred = append(deferred, fmt.Errorf("拍卖托管接收角色不可用：%s", id))
			continue
		}
		if _, err := owner.ReturnExpiredListings(ctx, id, actor.AccountID); err != nil {
			deferred = append(deferred, err)
			continue
		}
		if err := s.deliverCurrentAuctionPendingBuyerMailsLocked(ctx, owner, id); err != nil {
			deferred = append(deferred, err)
		}
		if err := s.deliverCurrentAuctionPendingPayoutsLocked(ctx, owner, id); err != nil {
			deferred = append(deferred, err)
		}
	}
	escrow, _, err = r.Inventory.Load(ctx, auction.EscrowCharacterID)
	pending := false
	for key := range escrow.Slots {
		pending = pending || currentAuctionPendingKey(key)
	}
	gold, ownerErr := s.currentGoldConsignOwner()
	if ownerErr == nil {
		goldPending, e := gold.Maintain(ctx)
		pending = pending || goldPending
		deferred = append(deferred, e)
	} else if currentGoldConsignPending(escrow.Slots) {
		pending = true
		deferred = append(deferred, ownerErr)
	}
	return pending, errors.Join(append(deferred, err)...)
}

func currentAuctionPendingKey(key string) bool {
	return strings.HasPrefix(key, "auction:listing:") || strings.HasPrefix(key, "auction:payout:") || strings.HasPrefix(key, "auction:delivery:")
}
func currentGoldConsignPending(slots map[string]dnfrepo.ItemStack) bool {
	for k := range slots {
		if strings.HasPrefix(k, "goldconsign:listing:") || strings.HasPrefix(k, "goldconsign:payout:") {
			return true
		}
	}
	return false
}
