// Exact festival renewal allowlist, approved for public source distribution.
package dnfbridge

import "strings"

func privateFestivalInstanceNeedsRenewal(d dungeonDropItemDefinition, instance uint32) bool {
	if d.DailyDeleteItem || d.UsablePeriodDays != 0 || d.ExpirationFromUsablePeriod || currentPVFExpirationUnix(d.ExpirationDate) != 1861826400 {
		return false
	}
	for _, r := range privateFestivalRetiredDates {
		if d.ItemID == r.id && strings.EqualFold(strings.ReplaceAll(d.PVFPath, "\\", "/"), r.path) && instance == r.previous {
			return true
		}
	}
	return false
}

var privateFestivalRetiredDates = []struct {
	id       uint32
	path     string
	previous uint32
}{{490700585, "stackable/cash/chn_490700585.stk", 1480543200},
	{490700586, "stackable/cash/chn_490700586.stk", 1480543200},
	{490700587, "stackable/cash/chn_490700587.stk", 1480543200},
	{490700588, "stackable/cash/chn_490700588.stk", 1480543200},
	{490700589, "stackable/cash/chn_490700589.stk", 1480543200},
	{490700590, "stackable/cash/chn_490700590.stk", 1480543200},
	{490700591, "stackable/cash/chn_490700591.stk", 1480543200},
	{490700592, "stackable/cash/chn_490700592.stk", 1480543200},
	{490700593, "stackable/cash/chn_490700593.stk", 1480543200},
	{490700594, "stackable/cash/chn_490700594.stk", 1480543200},
	{490700595, "stackable/cash/chn_490700595.stk", 1480543200},
	{490700596, "stackable/cash/chn_490700596.stk", 1480543200},
	{490700597, "stackable/cash/chn_490700597.stk", 1480543200},
	{490700598, "stackable/cash/chn_490700598.stk", 1480543200},
	{490700599, "stackable/cash/chn_490700599.stk", 1480543200},
	{490701323, "stackable/cash/chn_490701323.stk", 1509573600},
	{490701324, "stackable/cash/chn_490701324.stk", 1509573600},
	{490701325, "stackable/cash/chn_490701325.stk", 1509573600},
	{490702734, "stackable/cash/chn_490702734.stk", 1542232800},
	{490702737, "stackable/cash/chn_490702737.stk", 1542232800},
	{490702738, "stackable/cash/chn_490702738.stk", 1542232800},
	{490702739, "stackable/cash/chn_490702739.stk", 1542232800},
	{10008550, "stackable/cash/chn_20160126_new_year_package_event/chn_2016_3kingdoms_ancient_hammer_10008550.stk", 1461189600},
	{490002898, "stackable/490002001/chn_490002898.stk", 1479333600},
	{490009250, "stackable/490009001/chn_490009250.stk", 1542232800},
	{490009251, "stackable/490009001/chn_490009251.stk", 1542232800},
	{490009252, "stackable/490009001/chn_490009252.stk", 1542232800},
	{490009253, "stackable/490009001/chn_490009253.stk", 1542232800},
	{490009254, "stackable/490009001/chn_490009254.stk", 1542232800},
	{490009487, "stackable/490009001/chn_490009487.stk", 1542232800},
	{490700206, "stackable/cash/chn_490700206.stk", 1473285600},
	{490700207, "stackable/cash/chn_490700207.stk", 1473285600},
	{490700208, "stackable/cash/chn_490700208.stk", 1473285600},
	{490700749, "stackable/cash/chn_490700749.stk", 1490824800},
	{490700750, "stackable/cash/chn_490700750.stk", 1490824800},
	{490700779, "stackable/cash/chn_490700779.stk", 1490824800},
	{490700780, "stackable/cash/chn_490700780.stk", 1490824800},
	{490701326, "stackable/cash/chn_490701326.stk", 1509573600},
	{490701327, "stackable/cash/chn_490701327.stk", 1509573600},
	{490701768, "stackable/cash/chn_490701768.stk", 1522879200},
	{490701769, "stackable/cash/chn_490701769.stk", 1522879200},
	{490701770, "stackable/cash/chn_490701770.stk", 1522879200},
	{490701820, "stackable/cash/chn_490701820.stk", 1522879200},
	{490702373, "stackable/cash/chn_490702373.stk", 1534370400},
	{490702374, "stackable/cash/chn_490702374.stk", 1534370400},
	{490702742, "stackable/cash/chn_490702742.stk", 1542232800},
	{490702743, "stackable/cash/chn_490702743.stk", 1542232800},
	{490701752, "stackable/cash/chn_490701752.stk", 1522879200},
	{490701731, "stackable/cash/chn_490701731.stk", 1522879200},
	{490701732, "stackable/cash/chn_490701732.stk", 1522879200}}
