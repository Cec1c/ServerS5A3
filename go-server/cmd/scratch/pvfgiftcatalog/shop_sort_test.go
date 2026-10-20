package main

import "testing"

func TestGiftShelfPairedClassFilter(t *testing.T) {
	for _, test := range []struct {
		jobs           []string
		previous, want int32
	}{
		{[]string{"[mage]", "[creator mage]"}, -1, 3},
		{[]string{"[creator mage]", "[mage]"}, -1, 3},
		{[]string{"[swordman]", "[demonic swordman]"}, -1, 0},
		{[]string{"[all]"}, -1, -1},
		{[]string{"[at mage]"}, 8, 8},
		{[]string{"[fighter]", "[at fighter]"}, -1, -1},
	} {
		if got := giftShelfJob(test.jobs, test.previous); got != test.want {
			t.Fatalf("jobs=%v got=%d want=%d", test.jobs, got, test.want)
		}
	}
}
