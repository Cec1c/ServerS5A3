// pvfseriawelcome builds the private Seria welcome bundle without changing shared rewards.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	d "longheng.io/server/internal/modules/dnf/pvf"
	p "longheng.io/server/internal/platform/pvf"
)

type item struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Path   string  `json:"path"`
	Period []int64 `json:"period,omitempty"`
	Attach string  `json:"attach"`
	Expiry string  `json:"expiry,omitempty"`
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func text(a *p.Archive, name string) string { v, e := a.ReadText(name); check(e); return v }
func doc(a *p.Archive, name string) *d.Document {
	v, e := d.Parse(name, text(a, name))
	check(e)
	return v
}
func tokens(a *p.Archive, name string) []p.ScriptToken {
	raw, e := a.ReadRaw(name)
	check(e)
	if len(raw)%5 != 0 {
		panic("bad token stream")
	}
	out := []p.ScriptToken{}
	for i := 0; i < len(raw); i += 5 {
		t := p.ScriptToken{Type: raw[i], Value: int32(binary.LittleEndian.Uint32(raw[i+1:]))}
		if t.Type == 3 || t.Type == 6 {
			t.Text = a.ResolveString(int(t.Value))
		}
		out = append(out, t)
	}
	return out
}
func key(v string) p.ScriptToken { return p.ScriptToken{Type: 3, Text: "[" + v + "]"} }
func str(v string) p.ScriptToken { return p.ScriptToken{Type: 6, Text: v} }
func num(v int64) p.ScriptToken  { return p.ScriptToken{Type: 0, Value: int32(v)} }
func set(ts []p.ScriptToken, k string, vs ...p.ScriptToken) []p.ScriptToken {
	out := []p.ScriptToken{}
	found := false
	for i := 0; i < len(ts); {
		if ts[i].Type == 3 && ts[i].Text == "["+k+"]" {
			if found {
				panic("duplicate section " + k)
			}
			found = true
			out = append(out, key(k))
			out = append(out, vs...)
			i++
			for i < len(ts) && ts[i].Type != 3 {
				i++
			}
		} else {
			out = append(out, ts[i])
			i++
		}
	}
	if !found {
		out = append(out, key(k))
		out = append(out, vs...)
	}
	return out
}
func remove(ts []p.ScriptToken, ks ...string) []p.ScriptToken {
	out := []p.ScriptToken{}
	skip := false
	for _, t := range ts {
		if t.Type == 3 {
			skip = false
			for _, k := range ks {
				if t.Text == "["+k+"]" || t.Text == "[/"+k+"]" {
					skip = true
				}
			}
		}
		if !skip {
			out = append(out, t)
		}
	}
	return out
}
func main() {
	in := flag.String("pvf", "", "input archive")
	out := flag.String("out", "", "new archive")
	dir := flag.String("report-dir", "", "evidence directory")
	price := flag.Int64("price", 1, "gold price")
	audit := flag.Bool("audit", false, "inspect only")
	flag.Parse()
	a, e := p.Open(*in)
	check(e)
	if *dir == "" {
		panic("report-dir required")
	}
	check(os.MkdirAll(*dir, 0755))
	refs := map[int64]string{}
	equip := map[int64]item{}
	for _, root := range []string{"stackable", "equipment"} {
		for _, r := range d.ParseList(doc(a, root+"/"+root+".lst")) {
			v := path.Join(root, strings.ReplaceAll(r.Path, "\\", "/"))
			if a.FindFileIndex(v) < 0 {
				v = path.Join(path.Dir(v), "(r)"+path.Base(v))
			}
			refs[r.ID] = v
			if root == "equipment" {
				equip[r.ID] = item{ID: r.ID, Path: v}
			}
		}
	}
	b, e := os.ReadFile(filepath.Join(filepath.Dir(*dir), "seria-item-search.json"))
	check(e)
	var found []struct {
		ID   int64  `json:"item_id"`
		Name string `json:"name"`
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	check(json.Unmarshal(b, &found))
	permanent := map[string][]item{}
	all := []item{}
	for _, r := range found {
		if r.Kind != "equipment" || !strings.HasPrefix(r.Name, "灵跃之") {
			continue
		}
		v := doc(a, r.Path)
		name, _ := v.Text("name")
		attach, _ := v.Text("attach type")
		expiry, _ := v.Text("expiration date")
		it := item{r.ID, name, r.Path, v.Ints("usable period"), attach, expiry}
		equip[r.ID] = it
		all = append(all, it)
		if len(it.Period) == 0 && expiry == "" && !strings.Contains(name, "期限") {
			permanent[name] = append(permanent[name], it)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	writeJSON(filepath.Join(*dir, "weapons-audit.json"), all)
	for _, id := range []int64{490006983, 490003081, 490005052, 490003127, 490702722, 10163829, 400330047, 3330} {
		check(os.WriteFile(filepath.Join(*dir, fmt.Sprintf("%d.before.txt", id)), []byte(text(a, refs[id])), 0644))
	}
	if *audit {
		fmt.Printf("audited %d weapons; permanent names=%d\n", len(all), len(permanent))
		return
	}
	if *out == "" || strings.EqualFold(*in, *out) || *price < 1 {
		panic("new output and positive price required")
	}
	if _, e := os.Stat(*out); !os.IsNotExist(e) {
		panic("output already exists")
	}
	if a.Snapshot().Checksum != "94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7" {
		panic("unapproved baseline")
	}
	additions := []p.ScriptAddition{}
	added := []string{}
	changed := []string{}
	streams := map[string][]p.ScriptToken{}
	add := func(id int64, target, template string, ts []p.ScriptToken) {
		if _, ok := refs[id]; ok {
			panic(fmt.Sprintf("ID collision %d", id))
		}
		if a.FindFileIndex(target) >= 0 {
			panic("path collision")
		}
		refs[id] = target
		added = append(added, target)
		streams[target] = ts
		additions = append(additions, p.ScriptAddition{Path: target, TemplatePath: template, ReplaceTokens: true, AppendTokens: ts})
	}
	stackList := []p.ScriptToken{}
	equipmentList := []p.ScriptToken{}
	addStack := func(id int64, template string, ts []p.ScriptToken) {
		target := fmt.Sprintf("stackable/private_seria/%d.stk", id)
		add(id, target, template, ts)
		stackList = append(stackList, num(id), str(strings.TrimPrefix(target, "stackable/")))
	}
	weapon := remove(tokens(a, refs[490006983]), "usable period", "expiration date", "icon mark")
	weapon = set(weapon, "name", str("永久灵跃武器自选礼盒"))
	weapon = set(weapon, "explain", str("开启后，可按职业选择获得1件永久灵跃武器。每个礼盒只能选择1件。"))
	weapon = set(weapon, "attach type", str("[trade]"))
	weaponMap := map[int64]int64{}
	weaponAudit := []map[string]any{}
	inside := false
	row := 0
	for i, t := range weapon {
		if t.Type == 3 {
			inside = t.Text == "[equipment]"
			row = 0
			continue
		}
		if !inside {
			continue
		}
		if t.Type != 0 {
			panic("unexpected equipment row")
		}
		if row%2 == 0 {
			old := int64(t.Value)
			replacement, ok := weaponMap[old]
			if !ok {
				replacement = 990028001 + int64(len(weaponMap))
				weaponMap[old] = replacement
				original := equip[old]
				if !strings.HasPrefix(original.Name, "灵跃之") {
					panic(fmt.Sprintf("not audited Lingyue weapon %d", old))
				}
				ts := remove(tokens(a, refs[old]), "usable period", "expiration date", "daily delete item", "icon mark")
				name := strings.TrimSpace(strings.ReplaceAll(original.Name, "(期限)", ""))
				ts = set(ts, "name", str(name))
				ts = set(ts, "attach type", str("[trade]"))
				target := fmt.Sprintf("equipment/private_seria/%d.equ", replacement)
				add(replacement, target, refs[old], ts)
				equipmentList = append(equipmentList, num(replacement), str(strings.TrimPrefix(target, "equipment/")))
				weaponAudit = append(weaponAudit, map[string]any{"original_id": old, "new_id": replacement, "name": name, "path": target})
			}
			weapon[i] = num(replacement)
		}
		row++
	}
	addStack(990027002, refs[490006983], weapon)
	potion := remove(tokens(a, refs[490003127]), "daily delete item", "icon mark", "expiration date", "usable period")
	potion = set(potion, "attach type", str("[free]"))
	potion = set(potion, "explain", str("使用后，120分钟内获得2倍打怪经验。可交易。"))
	potion = set(potion, "stack limit", num(1000))
	addStack(990027003, refs[490003127], potion)
	avatar := remove(tokens(a, refs[490702722]), "expiration date", "usable period")
	addStack(990027004, refs[490702722], avatar)
	welcome := []p.ScriptToken{key("name"), str("赛丽亚欢迎礼盒（角色限购一次）"), key("explain"), str("每个角色限购1次，不会每日或每周重置。开启后获得：永久灵跃武器自选礼盒×1、远古精灵的超级秘药（120分钟，可交易）×3、梦幻克隆装扮套装礼盒×1、DPL_缓解疲劳之灵药×10、2015盛世狂欢称号×1、深渊派对邀请函×2000。"), key("grade"), num(1), key("rarity"), num(3), key("usable job"), str("[all]"), key("/usable job"), key("attach type"), str("[trade]"), key("minimum level"), num(1), key("weight"), num(1), key("price"), num(*price), key("value"), num(0), key("stack limit"), num(1), key("icon"), str("Item/stackable/consumption_cn.img"), num(1081), key("stackable type"), str("[booster]"), num(0), key("move wav"), str("BONE_TOUCH"), key("daily purchase limit"), str("character"), num(1), key("booster info")}
	rewards := [][2]int64{{990027002, 1}, {990027003, 3}, {990027004, 1}, {10163829, 10}, {400330047, 1}, {3330, 2000}}
	for _, r := range rewards {
		welcome = append(welcome, key("etc"), num(1), num(r[0]), num(100000), num(r[1]), key("/etc"))
	}
	welcome = append(welcome, key("/booster info"))
	addStack(990027001, refs[10005396], welcome)
	for _, v := range []struct {
		path string
		ts   []p.ScriptToken
	}{{"stackable/stackable.lst", stackList}, {"equipment/equipment.lst", equipmentList}} {
		changed = append(changed, v.path)
		additions = append(additions, p.ScriptAddition{Path: v.path, AppendToExisting: true, AppendTokens: v.ts})
	}
	shopPath := "itemshop/(r)equipmentshop7.shp"
	shop := tokens(a, shopPath)
	patched := []p.ScriptToken{}
	inList := false
	first := true
	removed := map[int64]int{}
	for _, t := range shop {
		if t.Type == 3 {
			inList = t.Text == "[item list]"
			patched = append(patched, t)
			if inList && first {
				patched = append(patched, num(990027001))
				first = false
			}
			continue
		}
		if inList && t.Type == 0 && (t.Value == 3299 || t.Value == 3300) {
			removed[int64(t.Value)]++
			continue
		}
		patched = append(patched, t)
	}
	if first || removed[3299] != 1 {
		panic("unexpected Seria shop baseline")
	}
	check(os.WriteFile(filepath.Join(*dir, "seria-shop.before.txt"), []byte(text(a, shopPath)), 0644))
	changed = append(changed, shopPath)
	streams[shopPath] = patched
	additions = append(additions, p.ScriptAddition{Path: shopPath, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: patched})
	data, e := a.RepackScriptAdditions(additions)
	check(e)
	next, e := p.OpenBytes(data)
	check(e)
	preserved, e := a.VerifyScriptReplacementAndAdditionPreservation(next, changed, added)
	check(e)
	for name, want := range streams {
		got := tokens(next, name)
		if len(got) != len(want) {
			panic("readback token count")
		}
		for i, v := range want {
			if v.Type != got[i].Type || (v.Type == 3 || v.Type == 6) && v.Text != got[i].Text || (v.Type != 3 && v.Type != 6) && v.Value != got[i].Value {
				panic("readback token mismatch " + name)
			}
		}
		f := filepath.Join(*dir, "after", filepath.FromSlash(name)+".txt")
		check(os.MkdirAll(filepath.Dir(f), 0755))
		check(os.WriteFile(f, []byte(text(next, name)), 0644))
	}
	check(os.WriteFile(*out, data, 0644))
	sum := sha256.Sum256(data)
	writeJSON(filepath.Join(*dir, "build.json"), map[string]any{"input_sha256": a.Snapshot().Checksum, "output_sha256": fmt.Sprintf("%x", sum), "output": *out, "price_gold": *price, "added_paths": added, "changed_paths": changed, "unchanged_files_preserved": preserved, "weapons": weaponAudit, "reward_rows": rewards, "removed_shop_counts": removed, "readback_tokens_equal": true})
	fmt.Printf("built %s sha256=%x new=%d changed=%d preserved=%d\n", *out, sum, len(added), len(changed), preserved)
}
func writeJSON(name string, v any) {
	b, e := json.MarshalIndent(v, "", "  ")
	check(e)
	check(os.WriteFile(name, b, 0644))
}
