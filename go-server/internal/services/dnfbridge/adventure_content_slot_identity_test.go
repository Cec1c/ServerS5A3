package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

// Offsets below are independently read through the distributed EXE's packet
// copy, manager-to-tab copy and native per-slot count branches. Slot numbers
// follow XUI IDs; the visual grid places the two tower rows above the raids.
func adventureNativeContentSlots() []struct {
	slot, label  string
	kind, offset int
} {
	return []struct {
		slot, label  string
		kind, offset int
	}{
		{"slot01_label", "推荐地下城通关", adventuregroup.ContentTypeRecommendedDungeon, 16},
		{"slot02_label", "无双决斗场获胜", adventuregroup.ContentTypePVPWin, 32},
		{"slot03_label", "安徒恩攻坚", adventuregroup.ContentTypeAnton, 24},
		{"slot04_label", "卢克攻坚", adventuregroup.ContentTypeLuke, 28},
		{"slot05_label", "绝望之塔100层", adventuregroup.ContentTypeDespairHundred, 36},
		{"slot06_label", "悲叹之塔100层", adventuregroup.ContentTypeGriefHundred, 40},
		{"slot08_label", "超时空之战", adventuregroup.ContentTypeFiendWar, 7420},
	}
}
func TestAdventureContentNativeSlotOneHot(t *testing.T) {
	slots := adventureNativeContentSlots()
	for kind := 0; kind < 7; kind++ {
		projection := adventuregroup.Projection{ConsecutiveLoginDays: 8}
		projection.ContentCounts[kind] = 1
		body := buildCurrentAdventureInfoBodyWithState(77, adventuregroup.Summary{}, nil, "", 20261110, projection)
		raw := body[18 : 18+7424]
		for _, s := range slots {
			want := uint32(0)
			if s.kind == kind {
				want = 1
			}
			if got := binary.LittleEndian.Uint32(raw[s.offset:]); got != want {
				t.Fatalf("kind=%d displayed by %s/%s: got %d want %d", kind, s.slot, s.label, got, want)
			}
		}
		if binary.LittleEndian.Uint32(raw[20:]) != 8 {
			t.Fatal("content count changed consecutive login days")
		}
	}
}
func TestAdventureContentRealPVFSlotLabelsMatchNativeIdentities(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.ReadText("ui/adventurer2018/tabinfo.xui")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimRight([]byte(text), "\x00\r\n ")))
	labels := map[string]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		tag, ok := token.(xml.StartElement)
		if !ok || tag.Name.Local != "CNUIControlText" {
			continue
		}
		var id, value string
		for _, attr := range tag.Attr {
			if attr.Name.Local == "ID" {
				id = attr.Value
			}
			if attr.Name.Local == "string" {
				value = attr.Value
			}
		}
		labels[id] = value
	}
	for _, s := range adventureNativeContentSlots() {
		if labels["STR:"+s.slot] != "STR:"+s.label {
			t.Fatalf("native slot label mismatch %s: %q", s.slot, labels["STR:"+s.slot])
		}
	}
}
