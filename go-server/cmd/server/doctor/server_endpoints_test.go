package main

import "testing"

func TestServerRolesStillRequireDistinctEndpoints(t *testing.T) {
	if err := checkDistinctRoleEndpoints("127.0.0.1:18110", "127.0.0.1:18111"); err != nil {
		t.Fatal(err)
	}
	if err := checkDistinctRoleEndpoints("127.0.0.1:18110", "127.0.0.1:018110"); err == nil {
		t.Fatal("channel and admin share a listening endpoint")
	}
}
