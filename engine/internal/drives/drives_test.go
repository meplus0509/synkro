package drives

import (
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	old := List
	defer func() { List = old }()
	List = func() ([]Drive, error) {
		return []Drive{
			{Root: `C:\`, Label: "Système", Serial: "1111-1111"},
			{Root: `F:\`, Label: "xxx", Serial: "ABCD-1234", Removable: true},
			{Root: `G:\`, Label: "BACKUP", Serial: "0000-0001", Removable: true},
			{Root: `H:\`, Label: "BACKUP", Serial: "0000-0002", Removable: true},
		}, nil
	}
	if d, err := Find(Volume{Serial: "abcd-1234", Label: "ancien nom"}); err != nil || d.Root != `F:\` {
		t.Fatalf("par numéro de série (casse et nom ignorés) : %v %v", d, err)
	}
	if d, err := Find(Volume{Label: "XXX"}); err != nil || d.Root != `F:\` {
		t.Fatalf("par nom : %v %v", d, err)
	}
	if _, err := Find(Volume{Serial: "9999-9999", Label: "xxx"}); err == nil || !strings.Contains(err.Error(), "pas branchée") {
		t.Fatalf("une autre clé portant le même nom ne doit pas être prise : %v", err)
	}
	if _, err := Find(Volume{Label: "BACKUP"}); err == nil || !strings.Contains(err.Error(), "plusieurs") {
		t.Fatalf("nom ambigu : %v", err)
	}
	if d, err := Find(Volume{Serial: "0000-0002", Label: "BACKUP"}); err != nil || d.Root != `H:\` {
		t.Fatalf("le numéro de série départage deux clés de même nom : %v %v", d, err)
	}
}

func TestForPath(t *testing.T) {
	old := List
	defer func() { List = old }()
	List = func() ([]Drive, error) {
		return []Drive{{Root: `C:\`, Label: "Système"}, {Root: `E:\`, Label: "Cle"}}, nil
	}
	if d, ok := ForPath(`c:\Users\demo`); !ok || d.Label != "Système" {
		t.Fatalf("C: attendu, obtenu %+v %v", d, ok)
	}
	if d, ok := ForPath(`E:\`); !ok || d.Label != "Cle" {
		t.Fatalf("E: attendu, obtenu %+v %v", d, ok)
	}
	if _, ok := ForPath(`F:\x`); ok {
		t.Fatal("F: ne devrait pas être trouvé")
	}
}
