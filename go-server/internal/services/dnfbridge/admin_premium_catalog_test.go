package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type membershipOverrideSource struct {
	dnfpvf.Source
	list string
}

func (s membershipOverrideSource) ReadText(path string) (string, error) {
	if path == "etc/vendingmachine.lst" {
		return s.list, nil
	}
	return s.Source.ReadText(path)
}

func TestRealPVFAdministrativeMembershipSeparatesActivation(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("DNFBRIDGE_REAL_PVF_SMOKE not set")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a}
	c, err := s.currentPremiumCatalog()
	if err != nil {
		t.Fatal(err)
	}
	original := map[int64]bool{}
	for _, v := range c.contractsByItem {
		if v.DurationSeconds > 0 {
			original[v.PremiumType] = true
		}
	}
	if original[17] {
		t.Fatal("test identity changed: real catalog already has 17")
	}
	before := len(c.contractsByItem)
	types, err := s.currentGMAdministrativePremiumTypes()
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{17}
	for typ := range original {
		want = append(want, typ)
	}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("types=%v want=%v", types, want)
	}
	if len(c.contractsByItem) != before {
		t.Fatal("admin registration changed item catalog")
	}
	if _, ok := c.contractsByItem[premium.BlackDiamondThirtyDayItemID]; ok {
		t.Fatal("unregistered historical activation item introduced")
	}
	if _, ok := currentPremiumInventoryContracts(c)[premium.BlackDiamondThirtyDayItemID]; ok {
		t.Fatal("unknown item activated")
	}
	for _, typ := range types {
		if typ == 580 || typ == 255 {
			t.Fatal("unknown/internal capability introduced")
		}
	}
	for _, tc := range []struct{ name, list string }{
		{"missing registered machine", ""},
		{"unknown machine id", "99 `pcroom.vm`\n"},
		{"wrong currency machine", "1 `pcroom3.vm`\n"},
		{"missing machine file", "1 `not-present.vm`\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if currentGMBlackDiamondMachineAvailable(membershipOverrideSource{a, tc.list}) {
				t.Fatal("unsupported resource enabled black diamond")
			}
		})
	}
	if currentGMBlackDiamondMachineAvailable(nil) {
		t.Fatal("nil source enabled membership")
	}
	t.Logf("real item-registered types=%d; administrative types=%v; item contracts unchanged=%d", len(original), types, before)
}

// This checks the server relog projection boundary, not acceptance by native EXE.
func TestAdminMembershipRelogProjectionBoundary(t *testing.T) {
	now := time.Unix(1791557436, 0)
	account := repo.AccountRecord{AccountID: "candidate-account", Metadata: map[string]string{}}
	result, err := premium.GrantAdminContract(&account, "candidate-character", 17, 30, "candidate-membership-17", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpireAt != now.Unix()+30*86400 {
		t.Fatal(result)
	}
	body := premium.SelectAckEntries(account, now)
	found := false
	for off := 1; off+9 <= len(body); off += 9 {
		if body[off] == 17 {
			if int64(binary.LittleEndian.Uint64(body[off+1:])) != 30*86400 {
				t.Fatalf("wrong 17 duration: %x", body)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no 17 in select body: %x", body)
	}
	replay, err := premium.GrantAdminContract(&account, "candidate-character", 17, 30, "candidate-membership-17", now)
	if err != nil || !replay.Replayed || replay.ExpireAt != result.ExpireAt {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	before := fmt.Sprint(account.Metadata)
	if _, err := premium.GrantAdminContract(&account, "candidate-character", 255, 30, "candidate-unknown-255", now); err == nil {
		t.Fatal("unknown type accepted")
	}
	if fmt.Sprint(account.Metadata) != before {
		t.Fatal("unknown rejection mutated metadata")
	}
	t.Logf("server select ACK premium vector: %x", body)
}
