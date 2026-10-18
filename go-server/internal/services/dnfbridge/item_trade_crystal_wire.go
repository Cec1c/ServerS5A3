package dnfbridge

import repo "longheng.io/server/internal/modules/dnf/repository"

// The current EXE's observed crystal-to-trade request uses virtual list36
// with the existing account crystal slot354..359. This alias is scoped to
// online trade endpoints; it is never a new persisted inventory list.
const currentItemTradeCrystalWireList byte = 36

func onlineItemTradeStorageSourceList(list byte, slot int16) (byte, bool) {
	switch list {
	case repo.MainInventoryListType, 1:
		return list, true
	case currentItemTradeCrystalWireList:
		if slot >= repo.CrystalWarehouseFirstSlot && slot <= repo.CrystalWarehouseLastSlot {
			return repo.MainInventoryListType, true
		}
	}
	return 0, false
}

func onlineItemTradeOfferWireSourceList(offer onlineItemTradeOffer) byte {
	if offer.wireSourceList == currentItemTradeCrystalWireList {
		return currentItemTradeCrystalWireList
	}
	return offer.sourceList
}
