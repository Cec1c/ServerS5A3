package tagtournament

import (
	"reflect"
	"testing"
)

func TestRewardIconsDeduplicateRandomCountsAndDoNotExposeMutableConfig(t *testing.T) {
	c := candidateCatalog(t)
	for _, mode := range []Mode{Normal, Hard} {
		rows, err := c.RewardDisplayIcons(mode, false)
		if err != nil || !reflect.DeepEqual(rows, []RewardRow{{ItemID: 100}, {ItemID: 101}}) {
			t.Fatal(rows, err)
		}
		rows[0].ItemID = 0
		fresh, err := c.RewardDisplayIcons(mode, false)
		if err != nil || fresh[0].ItemID != 100 {
			t.Fatal("显示查询修改了配置")
		}
	}
	if _, err := c.RewardDisplayIcons(Hard, true); err == nil {
		t.Fatal("困难不存在特殊关")
	}
}
