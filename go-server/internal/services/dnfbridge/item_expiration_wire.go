package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

func (s *Service) applyCurrentPVFUsePeriodsToEntriesAt(entries []currentItemListEntry, now time.Time) (int, error) {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return 0, err
	}
	return s.applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), entries, catalog, now)
}

func (s *Service) applyCurrentPVFUsePeriodsToEntriesWithLoadedCatalog(ctx context.Context, entries []currentItemListEntry) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return 0, err
	}
	return s.applyCurrentPVFUsePeriodsToEntriesWithCatalog(ctx, entries, catalog, time.Now().UTC())
}

func (s *Service) applyCurrentPVFUsePeriodsToEntriesWithCatalog(ctx context.Context, entries []currentItemListEntry, catalog *pvfDungeonDropCatalog, now time.Time) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if catalog == nil {
		return 0, errDungeonDropSourceRequired
	}
	patched := 0
	var errs []error
	for index := range entries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		itemID := binary.LittleEndian.Uint32(entries[index].data[0x02:0x06])
		definition, resolveErr := s.currentPVFDefinitionForItem(catalog, itemID)
		if resolveErr != nil {
			errs = append(errs, fmt.Errorf("resolve wire item=%d: %w", itemID, resolveErr))
			continue
		}
		rowChanged := false
		// 退役绝对日期策略：定义里的 [expiration date] 已经过去时先按策略续期，
		// 行里留着的旧退役时间戳（当年就是从这个日期抄下来的）随之被替换掉。
		// 这样即使盘上的堆栈还没被 repair 过，客户端这一屏也不会再看到"已超过使用期限"。
		rawDefinitionExpiration := definition.ExpirationDate
		if definition.DailyDeleteItem {
			// A persisted daily instance owns its deadline. The historical
			// absolute-date renewal policy cannot extend a service-day ticket.
			rawDefinitionExpiration = time.Time{}
		}
		definition.ExpirationDate = currentPVFRenewedRetiredExpirationDate(rawDefinitionExpiration, now)
		rawPvfUnix := currentPVFExpirationUnix(rawDefinitionExpiration)
		pvfUnix := currentPVFExpirationUnix(definition.ExpirationDate)
		rowUnix := binary.LittleEndian.Uint32(entries[index].data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4])
		effectiveUnix := pvfUnix
		if rowUnix != 0 {
			if !currentPVFInstanceExpirationSupersededByRenewal(rowUnix, rawDefinitionExpiration, definition.ExpirationDate) && currentGiftBoxHistoryExpiration(definition, rowUnix, entries[index].independentExpiration || !entries[index].expirationProvenanceKnown).IsZero() {
				// 实例自己的时间戳仍然权威（租用 24 小时、相对期限发放……）。
				effectiveUnix = rowUnix
			}
		}
		legacyWrongUnix := binary.LittleEndian.Uint32(entries[index].data[legacyWrongCurrentItemListExpireTimeOffset : legacyWrongCurrentItemListExpireTimeOffset+4])
		if legacyWrongUnix != 0 && (legacyWrongUnix == pvfUnix || legacyWrongUnix == rawPvfUnix) {
			binary.LittleEndian.PutUint32(entries[index].data[legacyWrongCurrentItemListExpireTimeOffset:legacyWrongCurrentItemListExpireTimeOffset+4], 0)
			rowChanged = true
		}
		if tailUnix := binary.LittleEndian.Uint32(entries[index].data[0x6E:0x72]); tailUnix != 0 &&
			(tailUnix == pvfUnix || tailUnix == rawPvfUnix) {
			binary.LittleEndian.PutUint32(entries[index].data[0x6E:0x72], 0)
			rowChanged = true
		}
		if effectiveUnix != 0 && rowUnix != effectiveUnix {
			binary.LittleEndian.PutUint32(entries[index].data[currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4], effectiveUnix)
			rowChanged = true
		}
		if definition.Kind == dungeonDropItemEquipment && currentItemListStackExtraIsAvatar(map[string]string{"equipment_type": definition.EquipmentType}) {
			// Template absolute-date policy is applied after the repository row
			// builder. Synchronize the avatar time scalar with that same effective
			// deadline; quantities and quality seeds never own this field.
			remainingSeconds := currentAvatarRemainingSecondsAt(effectiveUnix, now)
			if binary.LittleEndian.Uint32(entries[index].data[0x06:0x0A]) != remainingSeconds {
				binary.LittleEndian.PutUint32(entries[index].data[0x06:0x0A], remainingSeconds)
				rowChanged = true
			}
		}
		if isCurrentCeraShopCreatureItem(definition) {
			remainingSeconds := currentPetRemainingSecondsAt(effectiveUnix, now)
			if binary.LittleEndian.Uint32(entries[index].data[currentPetRemainSecondsOffset:currentPetRemainSecondsOffset+4]) != remainingSeconds {
				binary.LittleEndian.PutUint32(entries[index].data[currentPetRemainSecondsOffset:currentPetRemainSecondsOffset+4], remainingSeconds)
				rowChanged = true
			}
		}
		if definition.Kind == dungeonDropItemStackable && effectiveUnix != 0 {
			usePeriod := currentPVFStackableUsePeriodSeconds(time.Unix(int64(effectiveUnix), 0).UTC(), now)
			if binary.LittleEndian.Uint16(entries[index].data[0x0B:0x0D]) != usePeriod {
				binary.LittleEndian.PutUint16(entries[index].data[0x0B:0x0D], usePeriod)
				rowChanged = true
			}
		}
		if rowChanged {
			patched++
		}
	}
	return patched, errors.Join(errs...)
}

func (s *Service) applyCurrentPVFUsePeriodsToEntries(entries []currentItemListEntry) (int, error) {
	return s.applyCurrentPVFUsePeriodsToEntriesWithLoadedCatalog(context.Background(), entries)
}
