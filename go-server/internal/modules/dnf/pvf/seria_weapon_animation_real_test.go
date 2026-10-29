package pvf

import (
	"bytes"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"

	archive "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFPermanentSeriaWeaponAnimationRoots(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_SERIA_WEAPON_PVF")
	if file == "" {
		t.Skip("set DNFBRIDGE_SERIA_WEAPON_PVF to verify the permanent weapon resource contract")
	}
	a, err := archive.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.ReadText("equipment/equipment.lst")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse("equipment/equipment.lst", text)
	if err != nil {
		t.Fatal(err)
	}
	refs := make(map[int64]string)
	for _, entry := range ParseList(doc) {
		refs[entry.ID] = path.Join("equipment", strings.ToLower(strings.ReplaceAll(entry.Path, "\\", "/")))
	}
	for id := int64(990028001); id <= 990028037; id++ {
		p := refs[id]
		if !strings.HasPrefix(p, "equipment/character/") || path.Base(p) != "private_seria_"+strconv.FormatInt(id, 10)+".equ" {
			t.Fatalf("weapon %d has no character weapon animation root: %s", id, p)
		}
		old, err := a.ReadRaw("equipment/private_seria/" + strconv.FormatInt(id, 10) + ".equ")
		if err != nil {
			t.Fatal(err)
		}
		current, err := a.ReadRaw(p)
		if err != nil || !bytes.Equal(old, current) {
			t.Fatalf("weapon %d definition changed during relocation: %v", id, err)
		}
	}
	p := refs[990028005]
	if path.Dir(p) != path.Dir(refs[401040005]) {
		t.Fatalf("beam sword differs from its template animation root: %s", p)
	}
	weapon, err := a.ReadText(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, expired := range []string{"[usable period]", "[expiration date]", "[daily delete item]"} {
		if strings.Contains(weapon, expired) {
			t.Fatal("permanent weapon regained expiry", expired)
		}
	}
	lay, err := a.ReadText("equipment/character/swordman.lay")
	if err != nil {
		t.Fatal(err)
	}
	for _, motion := range []string{"Stay", "Attack1", "Attack2", "Attack3", "DashAttack", "JumpAttack"} {
		if !strings.Contains(lay, "%s/"+motion+".ani") {
			t.Fatal("motion is not in the equipped animation table", motion)
		}
		anim := path.Join(path.Dir(p), "beamswd", strings.ToLower(motion)+".ani")
		body, err := a.ReadText(anim)
		if err != nil || !strings.Contains(body, "[FRAME MAX]") {
			t.Fatalf("weapon motion missing %s: %v", anim, err)
		}
		if motion != "Stay" && !strings.Contains(body, "[ATTACK BOX]") {
			t.Fatalf("weapon motion has no native hit boxes: %s", anim)
		}
	}
}
