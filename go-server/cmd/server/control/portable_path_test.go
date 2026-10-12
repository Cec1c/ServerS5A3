package main

import "testing"

func TestManifestPathRejectsForeignPlatformEscapes(t *testing.T) {
	for _, path := range []string{`..\outside.exe`, "../outside.exe", `C:\outside.exe`,
		`C:outside.exe`, `\\server\share\outside.exe`, "/outside.exe", `sub\..\..\outside.exe`} {
		t.Run(path, func(t *testing.T) {
			if _, err := safeManifestPath(path); err == nil {
				t.Fatalf("accepted escaping manifest path %q", path)
			}
		})
	}
	for _, path := range []string{`data\dnf\Script.pvf`, "data/dnf/Script.pvf"} {
		if _, err := safeManifestPath(path); err != nil {
			t.Fatalf("rejected relative path: %v", err)
		}
	}
}
