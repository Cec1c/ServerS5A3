package dnfbridge

func currentPVPEmblemWalletEntry(total uint32) currentItemListEntry {
	var entry currentItemListEntry
	// sub_233B640 creates item 2 in list0/slot2; op14/sub_1D73120
	// updates that existing wallet through its count setter.
	entry.patchCore(2, 2, total)
	return entry
}
