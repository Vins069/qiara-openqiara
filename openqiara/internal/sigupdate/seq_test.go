package sigupdate

import "testing"

func TestSeqMarker(t *testing.T) {
	for _, n := range []int64{0, 1, 17, 99999999999} {
		got, ok := ParseSeqMarker(SeqMarker(n))
		if !ok || got != n {
			t.Errorf("%d -> %d %v", n, got, ok)
		}
	}
	if m := SeqMarker(17); m != "OQSEQ=17;" {
		t.Errorf("marqueur %q", m)
	}
	for _, bad := range []string{"", "17", "OQSEQ=;", "OQSEQ=17", "OQSEQ=-1;", "OQSEQ=a;", "XQSEQ=1;", "OQSEQ=1;x", "OQSEQ=999999999999999999999;"} {
		if _, ok := ParseSeqMarker(bad); ok {
			t.Errorf("%q accepté", bad)
		}
	}
}

func TestFindSeqInBinary(t *testing.T) {
	bin := func(parts ...string) []byte {
		var b []byte
		for _, p := range parts {
			b = append(b, p...)
			b = append(b, 0, 1, 2)
		}
		return b
	}
	cases := []struct {
		name string
		b    []byte
		want int64
		ok   bool
	}{
		{"une occurrence", bin("xx", "OQSEQ=17;", "yy"), 17, true},
		{"collée à d'autres chaînes (Go ne sépare pas)", []byte("abcOQSEQ=5;defOQSEQ="), 5, true},
		{"code seul : préfixe sans valeur", bin("OQSEQ=", ";"), 0, false},
		{"préfixe puis chiffres sans suffixe", bin("OQSEQ=12x"), 0, false},
		{"deux valeurs différentes : ambigu", bin("OQSEQ=1;", "OQSEQ=2;"), 0, false},
		{"deux fois la même", bin("OQSEQ=3;", "OQSEQ=3;"), 3, true},
		{"vide", nil, 0, false},
		{"préfixe en fin de fichier", []byte("OQSEQ="), 0, false},
	}
	for _, c := range cases {
		got, ok := FindSeqInBinary(c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s : %d %v, attendu %d %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
