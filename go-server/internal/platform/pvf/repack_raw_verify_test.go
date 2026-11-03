package pvf

import "testing"

func TestVerifyRawReplacementKeepsIdentityAndNameTable(t *testing.T) {
	a, err := OpenBytes(buildSyntheticNKPIArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.ReadRaw("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	data, err := a.RepackRaw([]RawReplacement{{Path: "dir/test.ani", Data: raw[5:]}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if n, e := a.VerifyRawReplacementPreservation(b, []string{"dir/test.ani"}); e != nil || n != 0 {
		t.Fatalf("valid existing-file replacement: %d %v", n, e)
	}
	for _, paths := range [][]string{nil, {"missing.ani"}, {"dir/test.ani", "dir/test.ani"}} {
		if _, e := a.VerifyRawReplacementPreservation(b, paths); e == nil {
			t.Fatalf("accepted invalid allowed paths: %v", paths)
		}
	}
	// Even a declared replacement cannot change the native file identity.
	saved := b.items[0]
	b.items[0].pathOffset++
	if _, e := a.VerifyRawReplacementPreservation(b, []string{"dir/test.ani"}); e == nil {
		t.Fatal("accepted changed native path identity")
	}
	b.items[0] = saved
	layout, err := b.layout()
	if err != nil {
		t.Fatal(err)
	}
	b.data[layout.nameOffset] ^= 1
	if _, e := a.VerifyRawReplacementPreservation(b, []string{"dir/test.ani"}); e == nil {
		t.Fatal("accepted changed encrypted string table")
	}
	b.data[layout.nameOffset] ^= 1
	if _, e := (*Archive)(nil).VerifyRawReplacementPreservation(b, []string{"dir/test.ani"}); e == nil {
		t.Fatal("accepted nil archive")
	}
}
