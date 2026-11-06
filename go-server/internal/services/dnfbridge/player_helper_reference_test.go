package dnfbridge

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlayerHelperReferenceRequiresExplicitEnable(t *testing.T) {
	for _, value := range []string{"", "off", "true", "1"} {
		t.Setenv("DNFBRIDGE_PLAYER_HELPER_REFERENCE", value)
		t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
		w := httptest.NewRecorder()
		(&accountAPIServer{}).handlePlayerHelper(w, httptest.NewRequest("POST", "/api/account/helper", strings.NewReader(`{"version":1,"action":"status"}`)))
		if w.Code != 404 || helperAutoPickupAvailable() {
			t.Fatalf("reference unexpectedly enabled by %q", value)
		}
	}
}
