package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRejectsPublicPlaintextAndRedirects(t *testing.T) {
	for _, endpoint := range []string{"http://8.8.8.8" + Path, "http://localhost" + Path, "https://user:pass@example.org" + Path, "https://example.org/wrong"} {
		if _, err := NewClient(endpoint, strings.Repeat("x", 32)); err == nil {
			t.Fatal("accepted unsafe endpoint", endpoint)
		}
	}
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c, err := NewClient(server.URL+Path, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Call(context.Background(), Request{Action: "online", CharacterID: 1}); err == nil || called {
		t.Fatal("redirect followed or accepted")
	}
}

func TestClientVersionAuthenticationAndFailure(t *testing.T) {
	good := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("missing credential")
		}
		var req Request
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Version != Version || req.CharacterID != 7 {
			t.Error("invalid request")
		}
		json.NewEncoder(w).Encode(Response{Version: Version, OK: good, Online: true})
	}))
	defer server.Close()
	c, _ := NewClient(server.URL+Path, strings.Repeat("x", 32))
	r, err := c.Call(context.Background(), Request{Action: "online", CharacterID: 7})
	if err != nil || !r.Online {
		t.Fatal(r, err)
	}
	good = false
	if _, err = c.Call(context.Background(), Request{Action: "online", CharacterID: 7}); err == nil {
		t.Fatal("failed response accepted")
	}
}
