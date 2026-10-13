package gmtool

import (
	"context"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"testing"
)

func TestGMLuckyStarUsesAccountWalletAndPVFLimit(t *testing.T) {
	h, _ := p3CargoGoldHarness(t, map[string]string{"rental_lucky_stars": "12", "account_cera": "100"})
	h.server.deps.PVF = func() (pvf.Source, error) {
		return readonlyTestSource{"etc/chnrentsystem/rentsysteminfo.etc": "[limit point] 400"}, nil
	}
	status, response := h.applyCurrency(t, map[string]any{"type": "luckystar", "value": 1000})
	requireFailure(t, status, response)
	value, err := accountLuckyStars(context.Background(), mustGroup(t, h.server), "acc-1")
	if value != 12 || err != nil {
		t.Fatalf("rejected write changed balance: %d %v", value, err)
	}
	status, response = h.applyCurrency(t, map[string]any{"type": "luckystar", "value": 400})
	requireOK(t, status, response)
	if response["luckyStar"] != float64(400) || response["limit"] != float64(400) {
		t.Fatal(response)
	}
	status, response = h.applyCurrency(t, map[string]any{"type": "luckystar", "amount": -2})
	requireOK(t, status, response)
	value, err = accountLuckyStars(context.Background(), mustGroup(t, h.server), "acc-1")
	if value != 398 || err != nil {
		t.Fatalf("%d %v", value, err)
	}
	account, _, _ := mustGroup(t, h.server).Account.Load(context.Background(), "acc-1")
	if account.Metadata["account_cera"] != "100" || account.HonorExp != 123 {
		t.Fatal("unrelated account state changed")
	}
	rows := h.server.accountListPayload(context.Background(), mustGroup(t, h.server), []webAccount{{PanelID: 1, AccountID: "acc-1"}})
	if rows[0]["luckyStar"] != uint32(398) {
		t.Fatal(rows)
	}
}
