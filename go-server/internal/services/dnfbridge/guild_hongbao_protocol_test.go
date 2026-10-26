package dnfbridge

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuildHongbaoProductionWireBoundsAndNativeFixtures(t *testing.T) {
	row := currentGuildHongbaoRow{ID: 1, ItemID: 490003266, Name: "会友", Portions: 5}
	rows := make([]currentGuildHongbaoRow, 500)
	for i := range rows {
		rows[i] = row
		rows[i].ID = uint32(i + 1)
	}
	list, err := buildCurrentGuildHongbaoListBody(30, 5000, 2, rows)
	if err != nil || len(list) != 20014 || binary.LittleEndian.Uint32(list[10:]) != 500 {
		t.Fatal(len(list), err)
	}
	if _, err := buildCurrentGuildHongbaoListBody(30, 0, 0, append(rows, row)); err == nil {
		t.Fatal("oversized list accepted")
	}
	for _, bad := range []currentGuildHongbaoRow{{ID: 0, ItemID: 1}, {ID: 1, ItemID: 0}, {ID: 1, ItemID: 1, Portions: 31}, {ID: 1, ItemID: 1, Name: strings.Repeat("会", 15)}} {
		if _, err := buildCurrentGuildHongbaoListBody(30, 0, 0, []currentGuildHongbaoRow{bad}); err == nil {
			t.Fatal("invalid row accepted")
		}
	}
	history := make([]currentGuildHongbaoClaimRow, 30)
	for i := range history {
		history[i] = currentGuildHongbaoClaimRow{Level: 90, Name: "会友", Amount: uint32(100 + i)}
	}
	hist, err := buildCurrentGuildHongbaoHistoryBody(history)
	if err != nil || len(hist) != 1084 || hist[39] != 0 || binary.LittleEndian.Uint32(hist[35:39]) != 100 {
		t.Fatal(len(hist), err)
	}
	if _, err := buildCurrentGuildHongbaoHistoryBody(append(history, history[0])); err == nil {
		t.Fatal("oversized history accepted")
	}
	for _, bad := range []currentGuildHongbaoClaimRow{{Amount: 0}, {Name: strings.Repeat("会", 15), Amount: 1}} {
		if _, err := buildCurrentGuildHongbaoHistoryBody([]currentGuildHongbaoClaimRow{bad}); err == nil {
			t.Fatal("invalid history accepted")
		}
	}
	row.Completed, row.Portions = true, 0
	pair, err := buildCurrentGuildHongbaoListBody(30, 5000, 2, []currentGuildHongbaoRow{rows[0], {ID: 2, ItemID: row.ItemID, Name: row.Name, Completed: true}})
	if err != nil || pair[53] != 0 || pair[93] != 1 {
		t.Fatal(pair, err)
	}
	if folder := os.Getenv("DNF_GUILD_HONGBAO_WIRE_DIR"); folder != "" {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"list-500": list, "list-pair": pair, "history-30": hist,
			"issue": buildCurrentGuildHongbaoIssueBody(490003266, 123), "claim": buildCurrentGuildHongbaoClaimBody(700)} {
			if err := os.WriteFile(filepath.Join(folder, name+".bin"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGuildHongbaoRequestExactShapes(t *testing.T) {
	for _, portions := range []byte{4, 5, 30, 31} {
		body := make([]byte, 5)
		binary.LittleEndian.PutUint32(body, 490003266)
		body[4] = portions
		item, count, err := parseCurrentGuildHongbaoIssue(body)
		if (err == nil) != (portions >= 5 && portions <= 30) {
			t.Fatal(portions, err)
		}
		if err == nil && (item != 490003266 || count != portions) {
			t.Fatal(item, count)
		}
		if _, _, err := parseCurrentGuildHongbaoIssue(append(body, 0)); err == nil {
			t.Fatal("trailing issue byte accepted")
		}
	}
	for _, body := range [][]byte{nil, make([]byte, 4), {1, 0, 0, 0, 0}} {
		if _, err := parseCurrentGuildHongbaoID(body); err == nil {
			t.Fatal("invalid id accepted")
		}
	}
	if id, err := parseCurrentGuildHongbaoID([]byte{1, 0, 0, 0}); err != nil || id != 1 {
		t.Fatal(id, err)
	}
}
