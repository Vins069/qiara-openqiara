package sigupdate

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
)

// Revue V17 : un manifeste signé ne doit pas pouvoir se lire de deux façons. Sur claude17,
// encoding/json acceptait une clé répétée (la dernière gagnait), une clé à la casse différente
// (« SEQ » pour « seq ») et des données après l'objet quand elles commençaient par « } » ou « ] ».
func TestManifestStrictParsing(t *testing.T) {
	r := newRelease(t)
	keys := []ed25519.PublicKey{r.pub}
	sha := strings.Repeat("ab", 32)
	body := `"channel":"stable","version":"v1","asset":"openqiarad-x","sha256":"` + sha + `","size":5000000`
	cases := map[string]string{
		"clé répétée":            `{"seq":3,` + body + `,"seq":900}`,
		"clé à la casse changée": `{"SEQ":3,` + body + `}`,
		"accolade en trop":       `{"seq":3,` + body + `}}`,
		"crochet après":          `{"seq":3,` + body + `}]`,
		"second objet":           `{"seq":3,` + body + `}{"seq":4}`,
		"UTF-8 invalide":         `{"seq":3,` + strings.Replace(body, `"v1"`, "\"v1\xff\"", 1) + `}`,
		"tableau":                `[{"seq":3,` + body + `}]`,
	}
	for name, mb := range cases {
		sig := EncodeSignature(ed25519.Sign(r.priv, SignedBytes([]byte(mb))))
		if _, err := Verify(keys, []byte(mb), sig); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s : %v, ErrBadManifest attendue", name, err)
		}
	}
	ok := `{"seq":3,` + body + `}` + "\n"
	if _, err := Verify(keys, []byte(ok), EncodeSignature(ed25519.Sign(r.priv, SignedBytes([]byte(ok))))); err != nil {
		t.Errorf("manifeste correct refusé : %v", err)
	}
}

// Revue V17 : la version (journal, MQTT, notifications) n'admettait sur claude17 que l'exclusion de
// \r, \n et NUL : séquences d'échappement de terminal, tabulations et inversion du sens d'écriture
// passaient.
func TestManifestVersionPrintable(t *testing.T) {
	for _, v := range []string{"v1\x1b[31m", "v1\tx", "v1‮x", "v1\u0007", "v1​", strings.Repeat("v", 65), ""} {
		m := goodManifest()
		m.Version = v
		if err := m.Validate(); !errors.Is(err, ErrBadManifest) {
			t.Errorf("version %q acceptée", v)
		}
	}
	for _, v := range []string{"v0.1.0-rc.25-claude-v17", "V17 (essai n°2) — 21/11", "v1+build.7"} {
		m := goodManifest()
		m.Version = v
		if err := m.Validate(); err != nil {
			t.Errorf("version %q refusée : %v", v, err)
		}
	}
}
