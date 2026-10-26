package guild

import (
	"fmt"
	"testing"
)

type practiceMonsterText string

func (s practiceMonsterText) ReadText(path string) (string, error) {
	if path != "etc/agitwarpassivemonsters.etc" {
		return "", fmt.Errorf("unexpected path %s", path)
	}
	return string(s), nil
}

func TestPracticeMonsterCatalogPreservesExplicitMapping(t *testing.T) {
	c, err := LoadPracticeMonsterCatalog(practiceMonsterText("[agitwar monster build]\n71021 70641 71031 70631 71061 70671\n[/agitwar monster build]"))
	if err != nil {
		t.Fatal(err)
	}
	if c[71021] != 70641 || c[71031] != 70631 || c[71061] != 70671 || c[71503] != 0 {
		t.Fatalf("mapping=%v", c)
	}
	for _, body := range []string{"", "[agitwar monster build]\n71021", "[agitwar monster build]\n0 1", "[agitwar monster build]\n71021 70641 71021 70642", "[agitwar monster build]\n`71021` 70641", "[agitwar monster build]\n71021 70641\n[agitwar monster build]\n71022 70642"} {
		if _, err := LoadPracticeMonsterCatalog(practiceMonsterText(body)); err == nil {
			t.Fatalf("invalid table accepted: %q", body)
		}
	}
}
