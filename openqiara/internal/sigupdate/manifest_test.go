package sigupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

func goodManifest() Manifest {
	return Manifest{Channel: "stable", Seq: 3, Version: "v1", IssuedAt: "2026-11-20T10:00:00Z", Asset: "openqiarad-x",
		SHA256: strings.Repeat("ab", 32), Size: 5_000_000}
}

func TestVerifyValid(t *testing.T) {
	r := newRelease(t)
	mb, sb := r.sign(goodManifest())
	m, err := Verify([]ed25519.PublicKey{r.pub}, mb, sb)
	if err != nil || m.Seq != 3 || m.Asset != "openqiarad-x" {
		t.Fatalf("m=%v err=%v", m, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	r := newRelease(t)
	mb, sb := r.sign(goodManifest())
	keys := []ed25519.PublicKey{r.pub}
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	_ = otherPub

	flip := func(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 1; return c }
	rawSig, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sb)))
	noCtx := EncodeSignature(ed25519.Sign(r.priv, mb)) // signé sans le contexte
	wrongKey := EncodeSignature(ed25519.Sign(otherPriv, SignedBytes(mb)))

	cases := []struct {
		name     string
		manifest []byte
		sig      []byte
	}{
		{"manifeste altéré", bytes.Replace(mb, []byte(`"seq": 3`), []byte(`"seq": 4`), 1), sb},
		{"manifeste altéré (1 bit)", flip(mb, 10), sb},
		{"signature altérée", mb, EncodeSignature(flip(rawSig, 5))},
		{"mauvaise clé", mb, wrongKey},
		{"sans contexte de domaine", mb, noCtx},
		{"signature tronquée", mb, EncodeSignature(rawSig[:40])},
		{"signature vide", mb, nil},
		{"signature pas en base64", mb, []byte("%%%%")},
		{"signature trop longue", mb, bytes.Repeat([]byte("A"), 2000)},
		{"manifeste vide", nil, sb},
		{"manifeste trop gros", bytes.Repeat([]byte(" "), 20000), sb},
	}
	for _, c := range cases {
		if _, err := Verify(keys, c.manifest, c.sig); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s : erreur %v, ErrBadSignature attendue", c.name, err)
		}
	}
}

func TestVerifyNoKeys(t *testing.T) {
	r := newRelease(t)
	mb, sb := r.sign(goodManifest())
	if _, err := Verify(nil, mb, sb); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyKeyRotation(t *testing.T) {
	old, new_ := newRelease(t), newRelease(t)
	keys := []ed25519.PublicKey{old.pub, new_.pub}
	for _, r := range []*release{old, new_} {
		mb, sb := r.sign(goodManifest())
		if _, err := Verify(keys, mb, sb); err != nil {
			t.Errorf("clé de la liste refusée : %v", err)
		}
	}
	stranger := newRelease(t)
	mb, sb := stranger.sign(goodManifest())
	if _, err := Verify(keys, mb, sb); !errors.Is(err, ErrBadSignature) {
		t.Errorf("clé étrangère acceptée : %v", err)
	}
}

func TestParseManifestInvalid(t *testing.T) {
	mut := func(f func(*Manifest)) Manifest { m := goodManifest(); f(&m); return m }
	cases := map[string]Manifest{
		"seq 0":              mut(func(m *Manifest) { m.Seq = 0 }),
		"seq négatif":        mut(func(m *Manifest) { m.Seq = -2 }),
		"min_seq > seq":      mut(func(m *Manifest) { m.MinSeq = 9 }),
		"min_seq négatif":    mut(func(m *Manifest) { m.MinSeq = -1 }),
		"canal vide":         mut(func(m *Manifest) { m.Channel = "" }),
		"canal majuscules":   mut(func(m *Manifest) { m.Channel = "Stable" }),
		"asset avec /":       mut(func(m *Manifest) { m.Asset = "../etc/passwd" }),
		"asset vide":         mut(func(m *Manifest) { m.Asset = "" }),
		"asset avec espace":  mut(func(m *Manifest) { m.Asset = "a b" }),
		"asset trop long":    mut(func(m *Manifest) { m.Asset = strings.Repeat("a", 65) }),
		"sha majuscules":     mut(func(m *Manifest) { m.SHA256 = strings.Repeat("AB", 32) }),
		"sha court":          mut(func(m *Manifest) { m.SHA256 = "abcd" }),
		"taille trop petite": mut(func(m *Manifest) { m.Size = MinBinarySize - 1 }),
		"taille trop grande": mut(func(m *Manifest) { m.Size = MaxBinarySize + 1 }),
		"version vide":       mut(func(m *Manifest) { m.Version = "" }),
		"version à 2 lignes": mut(func(m *Manifest) { m.Version = "a\nb" }),
		"date illisible":     mut(func(m *Manifest) { m.IssuedAt = "hier" }),
	}
	r := newRelease(t)
	for name, m := range cases {
		mb, sb := r.sign(m)
		if _, err := Verify([]ed25519.PublicKey{r.pub}, mb, sb); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s : erreur %v, ErrBadManifest attendue", name, err)
		}
	}
	for name, raw := range map[string]string{
		"champ inconnu": `{"channel":"stable","seq":1,"version":"v","asset":"a","sha256":"` + strings.Repeat("a", 64) + `","size":2000000,"evil":1}`,
		"pas du JSON":   `seq=1`,
		"deux objets":   `{} {}`,
		"données après": `{"channel":"stable","seq":1,"version":"v","asset":"a","sha256":"` + strings.Repeat("a", 64) + `","size":2000000} x`,
	} {
		if _, err := ParseManifest([]byte(raw)); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s : %v", name, err)
		}
	}
}

func TestManifestName(t *testing.T) {
	for in, want := range map[string]string{"": "update.json", "stable": "update.json", "beta": "update-beta.json"} {
		if got := ManifestName(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestParsePublicKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, s := range []string{base64.StdEncoding.EncodeToString(pub), hex.EncodeToString(pub), " " + base64.StdEncoding.EncodeToString(pub) + "\n"} {
		k, err := ParsePublicKey(s)
		if err != nil || !bytes.Equal(k, pub) {
			t.Errorf("%q : %v", s, err)
		}
	}
	for _, s := range []string{"", "abc", base64.StdEncoding.EncodeToString(pub[:31])} {
		if _, err := ParsePublicKey(s); err == nil {
			t.Errorf("%q accepté", s)
		}
	}
}

// Les clés gravées doivent toutes être lisibles : une entrée abîmée serait ignorée sans bruit.
func TestProductionKeysParse(t *testing.T) {
	for i, s := range ProductionKeys {
		if _, err := ParsePublicKey(s); err != nil {
			t.Errorf("ProductionKeys[%d] illisible : %v", i, err)
		}
	}
	if len(Keys()) != len(ProductionKeys) {
		t.Errorf("Keys() : %d clés lues sur %d", len(Keys()), len(ProductionKeys))
	}
}

// Barrière de publication : tant qu'aucune clé de production n'est gravée, la mise à jour signée est
// désactivée (voir TestNoKeyDisablesEverything). Pour exiger la clé avant de livrer une version :
// SIGUPDATE_REQUIRE_PROD_KEY=1 go test ./internal/sigupdate
func TestProductionKeyPresent(t *testing.T) {
	if len(ProductionKeys) > 0 {
		return
	}
	if os.Getenv("SIGUPDATE_REQUIRE_PROD_KEY") == "1" {
		t.Fatal("AUCUNE clé de production gravée (internal/sigupdate/keys.go) : la mise à jour signée est inactive. " +
			"Créer la paire avec « oqsign keygen », graver la clé PUBLIQUE, jamais la privée.")
	}
	t.Log("aucune clé de production gravée : mise à jour signée désactivée (SIGUPDATE_REQUIRE_PROD_KEY=1 pour exiger la clé)")
}
