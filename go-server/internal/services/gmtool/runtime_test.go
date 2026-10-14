package gmtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestStandaloneRuntimeFailureDeniesOfflineWrites(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer remote.Close()
	client, err := adminapi.NewClient(remote.URL+adminapi.Path, strings.Repeat("x", 32))
	mustGM(t, err)
	s := &Server{cfg: Config{RequireOffline: true}, deps: Deps{Runtime: client}}
	if s.requireOffline(httptest.NewRecorder(), "test", 1) {
		t.Fatal("runtime failure treated as offline")
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	mustGM(t, g.Character.Save(ctx, repository.CharacterRecord{AccountID: "a", CharacterID: "1"}))
	if s.requireAccountOffline(ctx, httptest.NewRecorder(), "test", g, "a", 1) {
		t.Fatal("account runtime failure treated as offline")
	}
}

func TestStandaloneCubeProjectionFailureRollsBack(t *testing.T) {
	for _, mode := range []string{"unavailable", "empty"} {
		t.Run(mode, func(t *testing.T) {
			encoded := false
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request adminapi.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.Action == "encode_stack" {
					encoded = true
					if mode == "unavailable" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				_ = json.NewEncoder(w).Encode(adminapi.Response{Version: adminapi.Version, OK: true, Online: false})
			}))
			defer remote.Close()
			client, err := adminapi.NewClient(remote.URL+adminapi.Path, strings.Repeat("x", 32))
			mustGM(t, err)
			h := newP3CubeHarness(t, p3CubeFixture{slots: map[string]repository.ItemStack{
				repository.AccountSharedInventorySlotKey(repository.CrystalWarehouseFirstSlot): {ItemID: p3CubeBlack, Count: 7, RawEntry: []byte{1, 2}, Extra: map[string]string{"preserve": "yes"}},
			}})
			h.server.deps.Runtime = client
			h.server.cfg.RequireOffline = true
			before := h.accountInventory(t)
			_, response := h.setCube(t, map[string]any{"selection": 0, "count": 5})
			if !encoded || response["success"] != false {
				t.Fatalf("projection failure not exercised/rejected: encoded=%v response=%v", encoded, response)
			}
			if after := h.accountInventory(t); !reflect.DeepEqual(before, after) {
				t.Fatalf("failed projection changed durable inventory: before=%+v after=%+v", before, after)
			}
		})
	}
}
