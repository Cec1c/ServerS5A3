package channelcatalog

import "testing"

func TestBootstrapChannelUsesConfiguredOrdinaryFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels []Channel
		want     int
	}{
		{"legacy entrance retained", []Channel{{ID: 19, Type: 1, Group: "crack"}, {ID: 10, Type: 11, Group: "granfloris"}}, 19},
		{"single ordinary ten", []Channel{{ID: 23, Type: 23, Group: "anton_raid"}, {ID: 10, Type: 11, Group: "granfloris"}}, 10},
		{"ordinary eleven", []Channel{{ID: 11, Type: 11, Group: "granfloris"}}, 11},
		{"raid only", []Channel{{ID: 23, Type: 23, Group: "anton_raid"}, {ID: 28, Type: 32, Group: "luke_raid"}}, 0},
		{"oversized ordinary", []Channel{{ID: 300, Type: 11, Group: "granfloris"}}, 0},
		{"nil", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Catalog{channels: tc.channels, byID: map[int]Channel{}}
			for _, row := range tc.channels {
				c.byID[row.ID] = row
			}
			got, ok := c.BootstrapChannel(19)
			if ok != (tc.want != 0) || got.ID != tc.want {
				t.Fatalf("got %+v exists=%t; want %d", got, ok, tc.want)
			}
		})
	}
	var empty *Catalog
	if _, ok := empty.BootstrapChannel(19); ok {
		t.Fatal("nil catalog invents an entrance")
	}
}
