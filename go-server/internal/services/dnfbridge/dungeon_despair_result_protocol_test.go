package dnfbridge

import (
	"bytes"
	"errors"
	"testing"
)

func TestCurrentDespairResultMsgIDMatchesVerifiedRegistration(t *testing.T) {
	if currentDespairResultMsgID != 348 {
		t.Fatalf("despair result msg id=%d want=348", currentDespairResultMsgID)
	}
}

func TestBuildCurrentDespairResultBodyWithoutRewards(t *testing.T) {
	body, err := buildCurrentDespairResultBody(0, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x00, 0x00, 0x00, 0x00, // clear time 0
		0x01, 0x00, // cleared floor 1
		0x00, // reward count 0
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("body=% x want=% x", body, want)
	}
}

func TestBuildCurrentDespairResultBodyWithOneReward(t *testing.T) {
	body, err := buildCurrentDespairResultBody(123456, 5, []currentDespairResultItem{{ItemID: 101, Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x40, 0xe2, 0x01, 0x00, // clear time 123456
		0x05, 0x00, // cleared floor 5
		0x01,                   // reward count 1
		0x65, 0x00, 0x00, 0x00, // item id 101
		0x02, 0x00, 0x00, 0x00, // quantity 2
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("body=% x want=% x", body, want)
	}
}

func TestBuildCurrentDespairResultBodyWithMultipleRewards(t *testing.T) {
	body, err := buildCurrentDespairResultBody(0xffffffff, 100, []currentDespairResultItem{
		{ItemID: 0x01020304, Quantity: 255},
		{ItemID: 0xffffffff, Quantity: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0xff, 0xff, 0xff, 0xff, // clear time 0xffffffff
		0x64, 0x00, // cleared floor 100
		0x02,                   // reward count 2
		0x04, 0x03, 0x02, 0x01, // item id 0x01020304
		0xff, 0x00, 0x00, 0x00, // quantity 255
		0xff, 0xff, 0xff, 0xff, // item id 0xffffffff
		0x01, 0x00, 0x00, 0x00, // quantity 1
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("body=% x want=% x", body, want)
	}
}

func TestBuildCurrentDespairResultBodyAcceptsOneByteRewardCountBoundary(t *testing.T) {
	items := make([]currentDespairResultItem, currentDespairResultMaximumItems)
	for index := range items {
		items[index] = currentDespairResultItem{ItemID: uint32(index) + 1, Quantity: 1}
	}
	body, err := buildCurrentDespairResultBody(7, 100, items)
	if err != nil {
		t.Fatal(err)
	}
	wantSize := 4 + 2 + 1 + len(items)*8
	if len(body) != wantSize || body[6] != 0xff {
		t.Fatalf("boundary body size=%d count=%d want size=%d count=255", len(body), body[6], wantSize)
	}
	tail := []byte{
		byte(currentDespairResultMaximumItems), 0x00, 0x00, 0x00, // last item id 255
		0x01, 0x00, 0x00, 0x00, // last quantity 1
	}
	if !bytes.Equal(body[len(body)-8:], tail) {
		t.Fatalf("tail=% x want=% x", body[len(body)-8:], tail)
	}
}

func TestBuildCurrentDespairResultBodyRejectsInvalidArguments(t *testing.T) {
	tooManyItems := make([]currentDespairResultItem, currentDespairResultMaximumItems+1)
	for index := range tooManyItems {
		tooManyItems[index] = currentDespairResultItem{ItemID: uint32(index) + 1, Quantity: 1}
	}
	cases := []struct {
		name         string
		clearTimeMS  uint32
		clearedFloor uint16
		items        []currentDespairResultItem
		want         error
	}{
		{
			name:         "floor below the tower domain",
			clearedFloor: 0,
			want:         errCurrentDespairResultFloor,
		},
		{
			name:         "floor above the tower domain",
			clearedFloor: currentDespairResultMaximumFloor + 1,
			want:         errCurrentDespairResultFloor,
		},
		{
			name:         "zero item id",
			clearedFloor: 30,
			items:        []currentDespairResultItem{{ItemID: 0, Quantity: 1}},
			want:         errCurrentDespairResultItem,
		},
		{
			name:         "zero quantity",
			clearedFloor: 30,
			items:        []currentDespairResultItem{{ItemID: 101, Quantity: 0}},
			want:         errCurrentDespairResultItem,
		},
		{
			name:         "zero item id in a later row",
			clearedFloor: 30,
			items: []currentDespairResultItem{
				{ItemID: 101, Quantity: 1},
				{ItemID: 102, Quantity: 0},
			},
			want: errCurrentDespairResultItem,
		},
		{
			name:         "reward count exceeds one byte",
			clearedFloor: 30,
			items:        tooManyItems,
			want:         errCurrentDespairResultCount,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := buildCurrentDespairResultBody(testCase.clearTimeMS, testCase.clearedFloor, testCase.items)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error=%v want=%v", err, testCase.want)
			}
			if body != nil {
				t.Fatalf("rejected arguments produced body=% x", body)
			}
		})
	}
}
