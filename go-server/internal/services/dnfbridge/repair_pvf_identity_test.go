package dnfbridge

// Repair tests accept only the archived repair resource and the exact resource
// read back from the live signed channel (sequence 83) on 2026-10-22. Assertions
// on recipes, items, maps and transaction results still run for either archive.
func knownRepairPVFChecksum(checksum string) bool {
	switch checksum {
	case "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348",
		"94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7":
		return true
	default:
		return false
	}
}
