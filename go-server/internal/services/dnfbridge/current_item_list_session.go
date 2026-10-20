package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
)

// Marks that this body includes the authoritative character gold row.
const currentGoldWalletProjectionSource = "character_gold_wallet_projection"

func (s *Service) buildCurrentItemListBodyForSession(ctx context.Context, session *gameSession, listType byte) ([]byte, string, int, bool) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, "", 0, false
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	if listType == 3 {
		return s.buildCurrentEquippedItemListBodyForSession(ctx, session)
	}
	// List 12 is an account-owned container.  It used to fall through to the
	// selected character inventory/settings records, which made its header
	// change when a different character logged in and also discarded every
	// account-cargo item.  The current EXE's list-12 reader still uses the
	// ordinary 0x77 rows, but both the header and the rows are account scoped.
	if listType == 12 {
		return s.buildCurrentAccountCargoItemListBodyForSession(ctx, session, repos)
	}
	if listType == dnfrepo.MainInventoryListType {
		accountID := strings.TrimSpace(s.accountIDForSession(session))
		owner, err := dnfinventory.NewOwner(repos)
		// 这两个迁移各自开一次事务并读整份角色背包/账号金库。它们原本在**每次**
		// 主背包包体构建时都跑一遍 —— 而开盒子、穿时装这类操作每次都会重建主背包，
		// 于是高频操作被两次无意义的事务拖慢。同一个角色在本会话里只需迁移一次。
		if err == nil && !session.itemListMigrationsDone {
			err = owner.MigrateAccountSharedSlots(ctx, accountID, characterID)
		}
		// A healthy absent character has no shared slots to migrate; still
		// read its true account warehouse before projecting list 0.
		if err != nil && !errors.Is(err, dnfinventory.ErrInventoryNotFound) {
			s.logPacketEvent("game-upper-current-item-list-account-inventory-migration-failed",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"account_id", accountID,
				"list_type", listType,
				"err", err)
			return nil, "", 0, false
		}
		var reviveCoinMigration dnfrevivecoin.Consolidation
		var reviveCoinErr error
		if !session.itemListMigrationsDone {
			reviveCoinMigration, reviveCoinErr = owner.MigrateReviveCoinWallet(ctx, characterID)
		}
		session.itemListMigrationsDone = true
		if reviveCoinErr != nil {
			// Slot 1 is reserved for the current revive-coin wallet. Never
			// overwrite an unrelated row if legacy data violates that
			// invariant; keep serving the inventory and expose the conflict.
			s.logPacketEvent("game-upper-current-revive-coin-wallet-migration-failed",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"list_type", listType,
				"err", reviveCoinErr)
		} else if reviveCoinMigration.Changed {
			s.logPacketEvent("game-upper-current-revive-coin-wallet-migrated",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"list_type", listType,
				"wallet_slot", 1,
				"removed_rows", reviveCoinMigration.RemovedRows,
				"converted_consumable_rows", reviveCoinMigration.ConvertedConsumableRows,
				"converted_consumable_units", reviveCoinMigration.ConvertedConsumableUnits,
				"wallet_total", reviveCoinMigration.Total,
				"source", "repository_backed_fixed_slot1_and_coin_general_consolidation")
		}
		catalog, catalogErr := s.currentPVFItemCatalog()
		if catalogErr != nil {
			s.logPacketEvent("game-upper-current-gold-wallet-slot-repair-catalog-failed",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"list_type", listType,
				"err", catalogErr)
		} else {
			goldWalletRepair, repairErr := s.repairCurrentGoldWalletReservedSlot(ctx, repos, characterID, catalog)
			if repairErr != nil {
				s.logPacketEvent("game-upper-current-gold-wallet-slot-repair-failed",
					"conn_id", session.connID,
					"char_id", session.selectedCharacterID,
					"list_type", listType,
					"item_id", goldWalletRepair.ItemID,
					"from_slot", goldWalletRepair.FromSlot,
					"to_slot", goldWalletRepair.ToSlot,
					"err", repairErr)
			} else if goldWalletRepair.Changed {
				s.logPacketEvent("game-upper-current-gold-wallet-slot-repaired",
					"conn_id", session.connID,
					"char_id", session.selectedCharacterID,
					"list_type", listType,
					"item_id", goldWalletRepair.ItemID,
					"from_slot", goldWalletRepair.FromSlot,
					"to_slot", goldWalletRepair.ToSlot,
					"source", "runtime_pvf_page_and_atomic_inventory_owner_relocation")
			}
		}
		if activationErr := s.reconcileCurrentPremiumInventoryBeforeList(
			ctx,
			session,
			"list0_snapshot_before_client_projection",
			true,
		); activationErr != nil {
			// Keep the recoverable item row when PVF/account persistence is
			// unavailable. A later list snapshot or character selection retries
			// the same conversion without losing either the item or its days.
			s.logPacketEvent("game-upper-current-premium-inventory-auto-activation-deferred",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"account_id", accountID,
				"list_type", listType,
				"err", activationErr,
				"fallback", "retain_recoverable_contract_item")
		}
		s.reconcileCurrentPVFItemExpirationsBestEffort(session, repos, characterID, accountID)
	}
	entries, source, known, inventorySnapshotErr := currentItemListEntriesFromInventorySnapshot(ctx, repos.Inventory, characterID, listType)
	if inventorySnapshotErr != nil {
		s.logPacketEvent("game-upper-current-item-list-inventory-load-failed",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"list_type", listType,
			"err", inventorySnapshotErr)
		return nil, source, 0, false
	}
	if (!known || len(entries) == 0) && repos.LegacyInventory != nil {
		legacyEntries, err := currentItemListEntriesFromLegacy(ctx, repos.LegacyInventory, characterID, listType)
		if err != nil {
			s.logPacketEvent("game-upper-current-item-list-legacy-load-failed",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"list_type", listType,
				"err", err)
		} else if len(legacyEntries) > 0 || !known {
			entries = legacyEntries
			source = "legacy_inventory"
			known = true
		}
	}
	if listType == dnfrepo.MainInventoryListType {
		var accountKnown bool
		entries, accountKnown = s.mergeCurrentAccountSharedItemListEntries(ctx, session, repos.AccountInventory, entries)
		// An account snapshot error must not install a partial list-0 view and
		// commit the selected-character bootstrap gate. A missing optional
		// repository retains the legacy path; a healthy absent record is known.
		if repos.AccountInventory != nil && !accountKnown {
			return nil, "", 0, false
		}
		if accountKnown && !known {
			source = "account_inventory"
			known = true
		}
		if repos.Character != nil {
			character, found, err := repos.Character.Load(ctx, characterID)
			if err != nil {
				s.logPacketEvent("game-upper-current-gold-wallet-load-failed",
					"conn_id", session.connID,
					"char_id", session.selectedCharacterID,
					"err", err)
			} else if found && character.Stats != nil {
				if balance, exists := character.Stats["finish_loading_currency_slot2_total"]; exists {
					projected, conflict := false, false
					for i, entry := range entries {
						if binary.LittleEndian.Uint16(entry.data[:2]) == 2 {
							if binary.LittleEndian.Uint32(entry.data[2:6]) == 2 {
								entries[i] = currentPVPEmblemWalletEntry(uint32(max(int64(0), min(balance, int64(1<<32-1)))))
								projected = true
							} else {
								conflict = true
							}
							break
						}
					}
					if !projected && !conflict {
						entries = append(entries, currentPVPEmblemWalletEntry(uint32(max(int64(0), min(balance, int64(1<<32-1))))))
						sortCurrentItemListEntries(entries)
						projected = true
					}
					if projected {
						known = true
						source += "+决斗徽章钱包"
					}
				}

				if gold, hasGold := character.Stats["gold"]; hasGold {
					walletProjected := false
					walletSlotConflict := false
					for index, entry := range entries {
						if int16(binary.LittleEndian.Uint16(entry.data[0x00:0x02])) == 0 {
							if binary.LittleEndian.Uint32(entry.data[0x02:0x06]) == 0 {
								// Go keeps gold in Character.Stats, unlike 86JP's
								// single persisted wallet row. Project the current
								// authoritative value over a stale wallet sentinel.
								entries[index] = currentGoldWalletItemListEntry(gold)
								walletProjected = true
							} else {
								walletSlotConflict = true
							}
							break
						}
					}
					if !walletProjected && !walletSlotConflict {
						entries = append(entries, currentGoldWalletItemListEntry(gold))
						sortCurrentItemListEntries(entries)
						walletProjected = true
					}
					if walletProjected {
						known = true
						if source == "" {
							source = currentGoldWalletProjectionSource
						} else {
							source += "+" + currentGoldWalletProjectionSource
						}
					} else if walletSlotConflict {
						s.logPacketEvent("game-upper-current-gold-wallet-slot-conflict",
							"conn_id", session.connID,
							"char_id", session.selectedCharacterID,
							"slot", 0,
							"reason", "non_wallet_inventory_row_owns_reserved_wallet_slot")
					}
				}
			}
		}
	}
	if !known {
		return nil, "", 0, false
	}
	patchedUsePeriods, usePeriodErr := s.applyCurrentPVFUsePeriodsToEntriesWithLoadedCatalog(ctx, entries)
	if usePeriodErr != nil {
		s.logPacketEvent("game-upper-current-item-use-period-wire-projection-failed",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"list_type", listType,
			"err", usePeriodErr)
	}
	if patchedUsePeriods > 0 {
		source += "+runtime_pvf_stackable_use_period"
	}
	containerState := s.loadCurrentItemListContainerState(ctx, repos.Settings, characterID, listType)
	return buildCurrentItemListBody(listType, entries, containerState), source, len(entries), true
}

func (s *Service) reconcileCurrentPVFItemExpirationsBestEffort(
	session *gameSession,
	repos dnfrepo.Group,
	characterID string,
	accountID string,
) {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logPacketEvent("game-upper-current-item-use-period-reconcile-failed",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"account_id", accountID,
			"inventory_repaired", 0,
			"warehouse_repaired", 0,
			"account_repaired", 0,
			"equipment_repaired", 0,
			"err", err)
		return
	}
	reconcileCtx, cancel := context.WithTimeout(context.Background(), currentItemListPVFReconcileTimeout)
	defer cancel()
	summary, reconcileErr := s.reconcileCurrentPVFItemExpirationsWithCatalog(reconcileCtx, repos, characterID, accountID, catalog)
	if reconcileErr != nil {
		s.logPacketEvent("game-upper-current-item-use-period-reconcile-failed",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"account_id", accountID,
			"inventory_repaired", summary.Inventory,
			"warehouse_repaired", summary.Warehouse,
			"account_repaired", summary.Account,
			"equipment_repaired", summary.Equipment,
			"err", reconcileErr)
		return
	}
	if summary.Total() > 0 {
		s.logPacketEvent("game-upper-current-item-use-period-reconciled",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"account_id", accountID,
			"inventory_repaired", summary.Inventory,
			"warehouse_repaired", summary.Warehouse,
			"account_repaired", summary.Account,
			"equipment_repaired", summary.Equipment,
			"source", "runtime_pvf_expiration_date_to_expire_time_and_stackable_u16_seconds")
	}
}
