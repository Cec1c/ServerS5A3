package chronicle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type chronicleAuditSource struct {
	path, text string
	base       dnfpvf.Source
}

func (s chronicleAuditSource) ReadText(path string) (string, error) {
	if path == s.path {
		return s.text, nil
	}
	return s.base.ReadText(path)
}

func TestCatalogPackedCharacterChecks(t *testing.T) {
	for _, section := range []string{
		"[character item check]\n2 1 `weapon` 5 1 `weapon`\n[/character item check]",
		"[character item check]\n2 1 `weapon`\n[/character item check]\n[character item check]\n5 1 `weapon`\n[/character item check]",
	} {
		source := testSource{"equipment/shared.equ": "[rarity] 5\n[durability] 45\n[equipment type] `[weapon]` 22\n[usable job] `[gunner]` `[at gunner]`\n[/usable job]\n" + section}
		catalog := NewCatalog(source, map[int64]Reference{625000: {Path: "equipment/shared.equ", Equipment: true}})
		e, err := catalog.Equipment(625000)
		if err != nil || len(e.CharacterChecks) != 2 {
			t.Fatalf("男女共用条件未完整解析：%+v %v", e.CharacterChecks, err)
		}
		m := Material{Checks: []Check{
			{Job: 2, Grow: 1, EquipmentType: "weapon", Skills: map[byte]int64{13: 1}},
			{Job: 5, Grow: 1, EquipmentType: "weapon", Skills: map[byte]int64{13: 2}},
			{Job: 2, Grow: 2, EquipmentType: "weapon", Skills: map[byte]int64{13: 3}},
		}}
		if !m.Allows(2, 1, e, 13) || !m.Allows(5, 1, e, 13) || m.Allows(2, 2, e, 13) || m.Allows(0, 1, e, 13) || m.Allows(2, 1, e, 31) {
			t.Fatal("共用装备的职业、转职或选项限制被放宽")
		}
	}
}

func TestCatalogPackedCharacterChecksRejectMalformedGroups(t *testing.T) {
	for _, value := range []string{"", "2 1", "2 1 `weapon` 5 1", "2 1 `weapon` `5` 1 `weapon`", "2 1 `weapon` 5 16 `weapon`", "2 1 `weapon` -1 1 `weapon`", "2 1 `weapon` 5 `1` `weapon`"} {
		t.Run(value, func(t *testing.T) {
			source := testSource{"equipment/shared.equ": "[rarity] 5\n[durability] 45\n[equipment type] `[weapon]`\n[character item check]\n" + value + "\n[/character item check]"}
			catalog := NewCatalog(source, map[int64]Reference{625000: {Path: "equipment/shared.equ", Equipment: true}})
			if _, err := catalog.Equipment(625000); !errors.Is(err, ErrRules) {
				t.Fatalf("不完整或越界条件未整体拒绝：%v", err)
			}
		})
	}
}

// 覆盖当前资源全部角色装备目录，既包括共用武器，也包括共用防具和首饰。
// 流式扫描避免为了取证把全部装备文本永久缓存；不连接玩家数据库。
func TestChronicleRealPVFAllSharedEquipmentChecks(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	indexText, err := archive.ReadText("equipment/equipment.lst")
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Parse("equipment/equipment.lst", indexText)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, row := range dnfpvf.ParseList(index) {
		p := strings.ToLower(strings.ReplaceAll(row.Path, "\\", "/"))
		if !strings.HasPrefix(p, "equipment/") {
			p = "equipment/" + p
		}
		ids[p] = row.ID
	}
	type finding struct {
		ID     int64   `json:"装备ID"`
		Name   string  `json:"名称"`
		Path   string  `json:"路径"`
		Type   string  `json:"部位"`
		Checks []Check `json:"职业条件"`
	}
	shared := []finding{}
	byType, byJob := map[string]int{}, map[string]int{}
	validated, indexed, unrelated, failed, transactions := 0, 0, 0, 0, 0
	var samples = map[int64]bool{625000: false, 115000: false, 100080578: false}
	err = archive.ScanTextPrefix("equipment/character", func(path, text string) error {
		id, listed := ids[strings.ToLower(path)]
		if !listed || !strings.HasSuffix(strings.ToLower(path), ".equ") {
			return nil
		}
		indexed++
		if !strings.Contains(strings.ToLower(text), "[character item check]") {
			return nil
		}
		doc, err := dnfpvf.Parse(path, text)
		if err != nil {
			return err
		}
		rarity, _ := doc.Int("rarity")
		if rarity != 5 {
			unrelated++
			return nil
		}
		catalog := NewCatalog(chronicleAuditSource{path, text, archive}, map[int64]Reference{id: {Path: path, Equipment: true}, 1256: {Path: "stackable/consumption_1256.stk"}})
		equipment, err := catalog.Equipment(id)
		if err != nil {
			failed++
			t.Errorf("异界装备解析失败：ID=%d %s %v", id, path, err)
			return nil
		}
		validated++
		if _, tracked := samples[id]; tracked {
			samples[id] = true
		}
		// 把文件明示的每组职业用于允许判断；再给额外职业、转职和部位技能，
		// 确保装备条件仍是独立限制，没有被“共用”变成通用装备。
		m := Material{}
		for _, check := range equipment.CharacterChecks {
			m.Checks = append(m.Checks, Check{Job: check.Job, Grow: check.Grow, EquipmentType: check.EquipmentType, Skills: map[byte]int64{8: 1}})
		}
		for job := byte(0); int(job) < len(jobNames); job++ {
			for grow := byte(0); grow < 16; grow++ {
				declared := false
				for _, check := range equipment.CharacterChecks {
					if check.Job == job && check.Grow == grow && check.EquipmentType == equipment.Type {
						declared = true
					}
				}
				want := declared && JobMatches(equipment.Jobs, job)
				if m.Allows(job, grow, equipment, 8) != want || m.Allows(job, grow, equipment, 31) {
					t.Errorf("职业/转职/选项限制不符：ID=%d job=%d grow=%d", id, job, grow)
				}
			}
		}
		packed := false
		for _, section := range doc.Sections {
			if normalized(section.Name) == "character item check" && section.End-section.Start > 3 {
				packed = true
			}
		}
		if packed {
			aura, err := catalog.Material(1256)
			if err != nil {
				return err
			}
			for _, check := range equipment.CharacterChecks {
				for _, rule := range aura.Checks {
					if rule.Job != check.Job || rule.Grow != check.Grow || rule.EquipmentType != equipment.Type || len(rule.Skills) == 0 {
						continue
					}
					numbers := []int{}
					for number := range rule.Skills {
						numbers = append(numbers, int(number))
					}
					sort.Ints(numbers)
					if t.Run(fmt.Sprintf("%d/%d/%d/%s", id, check.Job, check.Grow, equipment.Type), func(t *testing.T) {
						g, owner, cmd := fixture(t)
						cmd.Catalog = catalog
						cmd.Request = Request{MaterialSlot: 7, MaterialID: 1256, TargetSlot: 9, TargetID: id, Option: byte(numbers[0])}
						cmd.Identity = func(repository.CharacterRecord) (byte, byte, error) { return check.Job, check.Grow, nil }
						cmd.State = func(stack repository.ItemStack) (State, error) {
							n, _ := strconv.Atoi(stack.Extra["durability"])
							return State{Durability: uint16(n)}, nil
						}
						inv, _, err := g.Inventory.Load(context.Background(), "2")
						if err != nil {
							t.Fatal(err)
						}
						inv.Slots = map[string]repository.ItemStack{"0:7": {ItemID: 1256, Count: 3}, "0:9": {ItemID: id, Count: 1, Extra: map[string]string{"durability": fmt.Sprint(equipment.Durability)}}}
						if err := g.Inventory.Save(context.Background(), inv); err != nil {
							t.Fatal(err)
						}
						result, err := owner.Refine(context.Background(), cmd)
						if err != nil || !result.Succeeded || result.MaterialRemaining != 2 {
							t.Fatalf("共用装备事务拒绝：%+v %v", result, err)
						}
						stored, _, err := g.Inventory.Load(context.Background(), "2")
						var options []Option
						decodeErr := json.Unmarshal([]byte(stored.Slots["0:9"].Extra["options"]), &options)
						if err != nil || decodeErr != nil || stored.Slots["0:7"].Count != 2 || stored.Slots["0:9"].ItemID != id || len(options) != 1 || options[0].AuraID != 1256 || options[0].Job != check.Job || options[0].Grow != check.Grow || options[0].EquipmentType != equipment.TypeID {
							t.Fatal("扣气息或原位属性保存不符", err, decodeErr)
						}
					}) {
						transactions++
					}
					break
				}
			}
			name, _ := doc.Text("name")
			shared = append(shared, finding{id, name, path, equipment.Type, equipment.CharacterChecks})
			byType[equipment.Type]++
			seen := map[byte]bool{}
			for _, check := range equipment.CharacterChecks {
				if !seen[check.Job] {
					job := fmt.Sprintf("未知职业%d", check.Job)
					if int(check.Job) < len(jobNames) {
						job = jobNames[check.Job]
					}
					byJob[job]++
					seen[check.Job] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, seen := range samples {
		if !seen {
			t.Errorf("未覆盖现场样本%d", id)
		}
	}
	if validated == 0 || len(shared) == 0 {
		t.Fatal("真实资源没有命中异界或共用装备")
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].ID < shared[j].ID })
	report := map[string]any{"扫描已索引角色装备数": indexed, "通过的异界装备数": validated, "非异界条件装备数": unrelated, "解析失败数": failed, "原单组解析受影响装备数": len(shared), "受影响部位": byType, "受影响职业": byJob, "隔离精炼事务通过数": transactions, "受影响装备": shared}
	if output := os.Getenv("CHRONICLE_AUDIT_OUTPUT"); output != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("已扫描%d份已索引角色装备，%d份异界定义通过，发现%d份多组职业共用装备；部位=%v，职业=%v，失败=%d", indexed, validated, len(shared), byType, byJob, failed)
}

func TestChronicleRealPVFSharedEquipmentTransactions(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	indexText, err := archive.ReadText("equipment/equipment.lst")
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Parse("equipment/equipment.lst", indexText)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[int64]Reference{1256: {Path: "stackable/consumption_1256.stk"}}
	for _, row := range dnfpvf.ParseList(index) {
		if row.ID != 625000 && row.ID != 115000 {
			continue
		}
		p := row.Path
		if !strings.HasPrefix(p, "equipment/") {
			p = "equipment/" + p
		}
		refs[row.ID] = Reference{Path: p, Equipment: true}
	}
	catalog := NewCatalog(archive, refs)
	material, err := catalog.Material(1256)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{625000, 115000} {
		equipment, err := catalog.Equipment(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range equipment.CharacterChecks {
			for _, allowed := range material.Checks {
				if allowed.Job != check.Job || allowed.Grow != check.Grow || allowed.EquipmentType != equipment.Type || len(allowed.Skills) == 0 {
					continue
				}
				options := []int{}
				for number := range allowed.Skills {
					options = append(options, int(number))
				}
				sort.Ints(options)
				t.Run(fmt.Sprintf("%d/%d/%d", id, check.Job, check.Grow), func(t *testing.T) {
					g, owner, cmd := fixture(t)
					cmd.Catalog = catalog
					cmd.Request = Request{MaterialSlot: 7, MaterialID: 1256, TargetSlot: 9, TargetID: id, Option: byte(options[0])}
					cmd.Identity = func(repository.CharacterRecord) (byte, byte, error) { return check.Job, check.Grow, nil }
					cmd.State = func(stack repository.ItemStack) (State, error) {
						n, _ := strconv.Atoi(stack.Extra["durability"])
						return State{Durability: uint16(n)}, nil
					}
					inv, _, err := g.Inventory.Load(context.Background(), "2")
					if err != nil {
						t.Fatal(err)
					}
					inv.Slots = map[string]repository.ItemStack{"0:7": {ItemID: 1256, Count: 3}, "0:9": {ItemID: id, Count: 1, Extra: map[string]string{"durability": fmt.Sprint(equipment.Durability)}}}
					if err := g.Inventory.Save(context.Background(), inv); err != nil {
						t.Fatal(err)
					}
					result, err := owner.Refine(context.Background(), cmd)
					if err != nil || !result.Succeeded || result.MaterialRemaining != 2 {
						t.Fatalf("合法共用装备未完成精炼事务：%+v %v", result, err)
					}
					stored, _, err := g.Inventory.Load(context.Background(), "2")
					if err != nil || stored.Slots["0:7"].Count != 2 || !strings.Contains(stored.Slots["0:9"].Extra["options"], fmt.Sprint(1256)) {
						t.Fatal("事务未保存气息和原位属性", err)
					}
				})
				break
			}
		}
	}
}
