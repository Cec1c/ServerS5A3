package dnfbridge

import (
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGuildBenefitRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching Script.pvf")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := guild.LoadContentCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for i, days := range []int32{1, 7, 30} {
		for _, kind := range []byte{1, 3} {
			id := uint32(2 + i*3)
			value := int32(167)
			if kind == 1 {
				id++
				value = 10
			}
			v := c.Contents[id]
			if v.Type != kind || v.Value != value || v.Days != days || v.Price != uint32(days)*200 || v.Mode != 0 || v.Gold {
				t.Fatal("unexpected timed benefit", v)
			}
		}
	}
	if v := c.Contents[1]; v.Type != 5 || v.Value != 169 || v.Price != 36000 || v.Days != -1 || v.Gold {
		t.Fatal("unexpected permanent benefit", v)
	}
	text, err := source.ReadText("etc/variousbufflist.etc")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{167, 169} {
		block := regexp.MustCompile(`(?s)\[index\]\s*` + strconv.Itoa(id) + `\s+(.*?)\[/index\]`).FindStringSubmatch(text)
		if len(block) != 2 {
			t.Fatal("guild stat buff missing", id)
		}
		doc, err := pvf.Parse("guild_buff", block[1])
		if err != nil {
			t.Fatal(err)
		}
		group := "Guild_Status"
		if id == 169 {
			group += "1"
		}
		if !reflect.DeepEqual(doc.Texts("system group"), []string{group}) || !reflect.DeepEqual(doc.Ints("icon index"), []int64{206}) {
			t.Fatal("guild stat identity changed", id)
		}
		effects := regexp.MustCompile(`(?s)\[effect\](.*?)\[/effect\]`).FindAllStringSubmatch(block[1], -1)
		if len(effects) != 4 {
			t.Fatal("stat effect count changed", id)
		}
		for i, effect := range effects {
			e, err := pvf.Parse("effect", effect[1])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e.Texts("name"), []string{[]string{"physical_attack", "physical_defence", "magical_attack", "magical_defence"}[i]}) || !reflect.DeepEqual(e.Ints("param"), []int64{60, 0}) {
				t.Fatal("stat effect changed", id, i)
			}
		}
		apply := regexp.MustCompile(`(?s)\[apply type\](.*?)\[/apply type\]`).FindStringSubmatch(block[1])
		if len(apply) != 2 {
			t.Fatal("missing expiry mode")
		}
		e, err := pvf.Parse("apply", apply[1])
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e.Texts("type"), []string{"expire_time"}) || !reflect.DeepEqual(e.Ints("param"), []int64{0, 0}) {
			t.Fatal("expiry parameters changed")
		}
	}
}
