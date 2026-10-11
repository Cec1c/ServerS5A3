package gmtool

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func premiumGrantHarness(t *testing.T) *readonlyHarness {
	t.Helper()
	h := newReadonlyHarness(t, readonlyFixture{skipItems: true, character: repo.CharacterRecord{AccountID: "premium-account", Name: "契约测试"}})
	if err := h.group.Account.Save(context.Background(), repo.AccountRecord{AccountID: "premium-account", Metadata: map[string]string{"account_cera": "1234"}}); err != nil {
		t.Fatal(err)
	}
	h.server.deps.PremiumTypes = func() ([]int64, error) { return []int64{17, 22, 27, 84, 97, 33}, nil }
	return h
}

func premiumGrantPost(h *readonlyHarness, typ, days int64, id string) map[string]any {
	raw, _ := json.Marshal(map[string]any{"type": typ, "days": days, "requestId": id})
	req := httptest.NewRequest("POST", "/api/characters/1/premium", bytes.NewReader(raw))
	req.SetPathValue("id", "1")
	recorder := httptest.NewRecorder()
	h.server.handlePremiumGrant(recorder, req)
	var result map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	return result
}

func TestGMPremiumGrantIsAtomicDurableAndAccountShared(t *testing.T) {
	h := premiumGrantHarness(t)
	const id = "blackdiamond-admin-retry"
	results := make(chan map[string]any, 8)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- premiumGrantPost(h, 17, 30, id) }()
	}
	workers.Wait()
	close(results)
	applied := 0
	for result := range results {
		if result["success"] != true {
			t.Fatalf("grant failed: %+v", result)
		}
		if result["replayed"] == false {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("duplicate grants applied %d times", applied)
	}
	account, _, err := h.group.Account.Load(context.Background(), "premium-account")
	if err != nil {
		t.Fatal(err)
	}
	first := premium.ExpireAt(account, 17)
	if remaining := first - time.Now().Unix(); remaining < 30*86400-5 || remaining > 30*86400 {
		t.Fatalf("duration=%d", remaining)
	}
	if account.Metadata["account_cera"] != "1234" {
		t.Fatal("unrelated account wallet changed")
	}
	// Same receipt after a fresh server instance still replays rather than renewing.
	h.server = &Server{cfg: h.server.cfg, deps: h.server.deps}
	replay := premiumGrantPost(h, 17, 30, id)
	if replay["replayed"] != true {
		t.Fatal("restart lost durable idempotency", replay)
	}
	renewed := premiumGrantPost(h, 17, 30, id+"-renew")
	if renewed["success"] != true {
		t.Fatal(renewed)
	}
	account, _, _ = h.group.Account.Load(context.Background(), "premium-account")
	if got := premium.ExpireAt(account, 17); got != first+30*86400 {
		t.Fatalf("renewal=%d", got)
	}
	inventory, _, _ := h.group.Inventory.Load(context.Background(), "1")
	if len(inventory.Slots) != 0 {
		t.Fatal("admin entitlement unexpectedly created inventory items")
	}
}

func TestGMPremiumGrantValidatesResourceScopeAndReceipt(t *testing.T) {
	h := premiumGrantHarness(t)
	for _, tc := range []struct {
		typ, days int64
		id        string
	}{
		{580, 30, "unsupported-contract"},
		{17, 0, "invalid-zero-days"},
		{17, 3651, "invalid-long-days"},
		{17, 30, "tiny"},
	} {
		if result := premiumGrantPost(h, tc.typ, tc.days, tc.id); result["success"] != false {
			t.Fatalf("invalid request allowed: %+v", result)
		}
	}
	h.server.deps.PremiumTypes = func() ([]int64, error) { return []int64{22}, nil }
	if result := premiumGrantPost(h, 17, 30, "unregistered-blackdiamond"); result["success"] != false {
		t.Fatal(result)
	}
	h.server.deps.PremiumTypes = func() ([]int64, error) { return []int64{17, 22}, nil }
	if result := premiumGrantPost(h, 17, 30, "receipt-scope-test"); result["success"] != true {
		t.Fatal(result)
	}
	if result := premiumGrantPost(h, 22, 30, "receipt-scope-test"); result["success"] != false {
		t.Fatal("request ID rebound", result)
	}
	account, _, _ := h.group.Account.Load(context.Background(), "premium-account")
	if premium.ExpireAt(account, 22) != 0 {
		t.Fatal("rejected request altered other contract")
	}
}

func TestGMPremiumStatusReportsEachRegisteredContractExpiry(t *testing.T) {
	h := premiumGrantHarness(t)
	for _, typ := range []int64{17, 22, 27, 84, 97, 33} {
		if result := premiumGrantPost(h, typ, 30, "contract-audit-"+strconv.FormatInt(typ, 10)); result["success"] != true {
			t.Fatal(result)
		}
	}
	req := httptest.NewRequest("GET", "/api/characters/1/premium", nil)
	req.SetPathValue("id", "1")
	recorder := httptest.NewRecorder()
	h.server.handlePremiumStatus(recorder, req)
	var result struct {
		Success   bool `json:"success"`
		Contracts []struct {
			Type     int64 `json:"type"`
			Active   bool  `json:"active"`
			ExpireAt int64 `json:"expireAt"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || len(result.Contracts) != 6 {
		t.Fatalf("status=%s", recorder.Body.String())
	}
	for _, contract := range result.Contracts {
		if !contract.Active || contract.ExpireAt <= time.Now().Unix() {
			t.Fatalf("inactive contract %+v", contract)
		}
	}
}
