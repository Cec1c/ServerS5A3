package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/epicbook"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPrivateContent006ActualPVF(t *testing.T) {
	path := os.Getenv("PRIVATE_CONTENT006_PVF")
	if path == "" {
		t.Skip("candidate PVF required")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	text, e := a.ReadText("stackable/10304001/10304122.stk")
	if e != nil {
		t.Fatal(e)
	}
	doc, e := pvf.Parse("potion", text)
	if e != nil {
		t.Fatal(e)
	}
	effect, e := experiencebuff.Parse(doc)
	if e != nil || effect.Kind != experiencebuff.Quest || effect.BasisPoints != 10000 || effect.DurationMillis != 1800000 {
		t.Fatal(effect, e)
	}
	now := time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC)
	ch := repo.CharacterRecord{Level: 90}
	if e = experiencebuff.Record(&ch, effect, 10304122, now); e != nil {
		t.Fatal(e)
	}
	if experiencebuff.Bonus(ch, experiencebuff.Quest, 123456, now) != 123456 || experiencebuff.Bonus(ch, experiencebuff.Clear, 123456, now) != 0 || experiencebuff.Bonus(ch, experiencebuff.Quest, 123456, now.Add(30*time.Minute)) != 0 {
		t.Fatal("quest potion scope/duration")
	}
	book, e := epicbook.LoadCatalog(a)
	if e != nil || book.Len() != 966 {
		t.Fatal(book, e)
	}
	text, e = a.ReadText(epicbook.CatalogPath)
	if e != nil {
		t.Fatal(e)
	}
	doc, e = pvf.Parse(epicbook.CatalogPath, text)
	if e != nil {
		t.Fatal(e)
	}
	pairs := doc.Ints("piece list")
	for i := 0; i < len(pairs); i += 2 {
		r, ok := book.Lookup(uint32(pairs[i]))
		if !ok || r.PieceID != uint32(pairs[i+1]) || r.Materials[0].Count != 300 {
			t.Fatal("recipe", pairs[i], r)
		}
	}
	index, e := pvf.Build(context.Background(), a, pvf.BuildOptions{Lists: []string{"n_quest/quest.lst"}})
	if e != nil {
		t.Fatal(e)
	}
	q, e := quest.Load(context.Background(), index)
	if e != nil {
		t.Fatal(e)
	}
	for id, want := range map[int64]int64{5928: 15, 5929: 23, 649: 20, 676: 1, 650: 30, 677: 1, 2711: 2, 5851: 5, 5850: 3, 5852: 25} {
		d, ok := q.Find(id)
		if !ok {
			t.Fatal("quest absent", id)
		}
		found := false
		for i := 0; i < len(d.IntData); i += 2 {
			if d.IntData[i] == 3299 {
				found = true
				if d.IntData[i+1] != want {
					t.Fatal("requirement", id, d.IntData)
				}
			}
		}
		if !found {
			t.Fatal("void stone missing", id)
		}
	}
	t.Log("Real PVF: quest EXP +100%/30min with clear EXP unchanged; 966 recipes use 300 pieces; all 10 indexed void-stone quests have the new integer costs.")
}
