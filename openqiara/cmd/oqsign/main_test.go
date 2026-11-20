package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caligone/openqiara/internal/sigupdate"
)

func oq(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

// armBinary : faux ELF ARM 32 bits de 1,2 Mo portant le marqueur de seq (comme -X main.signedSeq).
func armBinary(seq int64, withMarker bool) []byte {
	b := make([]byte, 1_200_000)
	for i := range b {
		b[i] = byte(i * 7)
	}
	copy(b, []byte{0x7f, 'E', 'L', 'F', 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0x28, 0})
	if withMarker {
		copy(b[5000:], sigupdate.SeqMarker(seq))
	}
	return b
}

func setup(t *testing.T) (dir, key string) {
	t.Helper()
	dir = t.TempDir()
	key = filepath.Join(dir, "cle.oqkey")
	if code, _, e := oq(t, "keygen", "-out", key); code != 0 {
		t.Fatalf("keygen : %s", e)
	}
	return dir, key
}

func TestKeygenKeepsPrivateKeyPrivate(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "cle.oqkey")
	code, out, errOut := oq(t, "keygen", "-out", key)
	if code != 0 {
		t.Fatalf("code %d : %s", code, errOut)
	}
	st, err := os.Stat(key)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("droits du fichier : %v %v", st, err)
	}
	raw, _ := os.ReadFile(key)
	seed := strings.Fields(string(raw))[1]
	priv, err := loadPrivate(key)
	if err != nil {
		t.Fatal(err)
	}
	full := base64.StdEncoding.EncodeToString(priv)
	for _, s := range []string{seed, full} {
		if strings.Contains(out, s) || strings.Contains(errOut, s) {
			t.Fatal("la clé privée apparaît dans la sortie")
		}
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	if !strings.Contains(out, pub) {
		t.Error("la clé publique n'est pas affichée")
	}
	// pubkey redonne la même clé publique sans montrer la privée
	_, out2, _ := oq(t, "pubkey", "-key", key)
	if !strings.Contains(out2, pub) || strings.Contains(out2, seed) {
		t.Errorf("pubkey : %q", out2)
	}
}

func TestKeygenNeverOverwrites(t *testing.T) {
	_, key := setup(t)
	before, _ := os.ReadFile(key)
	if code, _, e := oq(t, "keygen", "-out", key); code == 0 {
		t.Fatal("clé existante écrasée")
	} else if !strings.Contains(e, "jamais écrasé") {
		t.Errorf("message : %s", e)
	}
	after, _ := os.ReadFile(key)
	if !bytes.Equal(before, after) {
		t.Error("le fichier a changé")
	}
	if code, _, _ := oq(t, "keygen"); code == 0 {
		t.Error("keygen sans -out accepté")
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	dir, key := setup(t)
	bin := filepath.Join(dir, "openqiarad-claude-v17")
	os.WriteFile(bin, armBinary(17, true), 0o755)
	outdir := filepath.Join(dir, "release")
	code, out, e := oq(t, "sign", "-key", key, "-in", bin, "-version", "v0.1.0-rc.23-claude-v17", "-seq", "17", "-min-seq", "0", "-outdir", outdir, "-issued-at", "2026-11-20T10:00:00Z")
	if code != 0 {
		t.Fatalf("sign : %s", e)
	}
	if strings.Contains(out, "oqkey") && strings.Contains(out, "seed") {
		t.Error("sortie suspecte")
	}
	mb, _ := os.ReadFile(filepath.Join(outdir, "update.json"))
	sb, _ := os.ReadFile(filepath.Join(outdir, "update.json.sig"))
	priv, _ := loadPrivate(key)
	// ce que la caméra accepte (même code que sigupdate.Updater)
	m, err := sigupdate.Verify([]ed25519.PublicKey{priv.Public().(ed25519.PublicKey)}, mb, sb)
	if err != nil || m.Seq != 17 || m.Asset != "openqiarad-claude-v17" || m.Channel != "stable" || m.Size != 1_200_000 {
		t.Fatalf("manifeste relu : %+v %v", m, err)
	}
	pubFile := filepath.Join(dir, "cle.pub")
	_, pubOut, _ := oq(t, "pubkey", "-key", key)
	os.WriteFile(pubFile, []byte(strings.Fields(pubOut)[0]+"\n"), 0o644)
	if code, out, e := oq(t, "verify", "-pub", pubFile, "-manifest", filepath.Join(outdir, "update.json"), "-bin", bin); code != 0 || !strings.Contains(out, "correspond") {
		t.Fatalf("verify : %d %s %s", code, out, e)
	}
	// une signature ou un manifeste altérés sont refusés
	os.WriteFile(filepath.Join(outdir, "update.json"), []byte(strings.Replace(string(mb), `"seq": 17`, `"seq": 18`, 1)), 0o644)
	if code, _, _ := oq(t, "verify", "-pub", pubFile, "-manifest", filepath.Join(outdir, "update.json")); code == 0 {
		t.Error("manifeste altéré accepté")
	}
	// une autre clé aussi
	_, key2 := setup(t)
	_, pub2, _ := oq(t, "pubkey", "-key", key2)
	os.WriteFile(filepath.Join(outdir, "update.json"), mb, 0o644)
	if code, _, _ := oq(t, "verify", "-pub", strings.Fields(pub2)[0], "-manifest", filepath.Join(outdir, "update.json")); code == 0 {
		t.Error("mauvaise clé acceptée")
	}
	// le binaire changé après la signature est repéré
	os.WriteFile(bin, armBinary(17, true)[:1_199_999], 0o755)
	if code, _, _ := oq(t, "verify", "-pub", pubFile, "-manifest", filepath.Join(outdir, "update.json"), "-bin", bin); code == 0 {
		t.Error("binaire modifié accepté")
	}
}

func TestSignRefusals(t *testing.T) {
	dir, key := setup(t)
	good := filepath.Join(dir, "good")
	os.WriteFile(good, armBinary(5, true), 0o755)
	nomark := filepath.Join(dir, "nomark")
	os.WriteFile(nomark, armBinary(5, false), 0o755)
	x86 := filepath.Join(dir, "x86")
	b := armBinary(5, true)
	b[18] = 0x3e
	os.WriteFile(x86, b, 0o755)
	script := filepath.Join(dir, "script")
	os.WriteFile(script, append([]byte("#!/bin/sh\n"), make([]byte, 1_200_000)...), 0o755)
	small := filepath.Join(dir, "small")
	os.WriteFile(small, armBinary(5, true)[:10_000], 0o755)

	loose := filepath.Join(dir, "loose.oqkey")
	raw, _ := os.ReadFile(key)
	os.WriteFile(loose, raw, 0o644)

	ok := []string{"-version", "v", "-seq", "5", "-outdir", filepath.Join(dir, "o")}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"seq différent de celui du binaire", []string{"sign", "-key", key, "-in", good, "-version", "v", "-seq", "6", "-outdir", dir + "/o"}, "porte le seq 5"},
		{"binaire sans seq", append([]string{"sign", "-key", key, "-in", nomark}, ok...), "aucun seq"},
		{"x86", append([]string{"sign", "-key", key, "-in", x86}, ok...), "non ARM"},
		{"script", append([]string{"sign", "-key", key, "-in", script}, ok...), "ELF"},
		{"trop petit", append([]string{"sign", "-key", key, "-in", small}, ok...), "taille"},
		{"seq 0", []string{"sign", "-key", key, "-in", good, "-version", "v", "-seq", "0"}, "-seq"},
		{"sans version", []string{"sign", "-key", key, "-in", good, "-seq", "5"}, "-version"},
		{"canal invalide", append([]string{"sign", "-key", key, "-in", good, "-channel", "X Y"}, ok...), "canal"},
		{"clé lisible par tous", append([]string{"sign", "-key", loose, "-in", good}, ok...), "lisible par d'autres"},
		{"clé absente", append([]string{"sign", "-key", dir + "/rien", "-in", good}, ok...), "no such file"},
		{"fichier de clé quelconque", append([]string{"sign", "-key", good, "-in", good}, ok...), "droits"},
	}
	for _, c := range cases {
		code, _, e := oq(t, c.args...)
		if code == 0 || !strings.Contains(e, c.want) {
			t.Errorf("%s : code %d, erreur %q (attendu : %q)", c.name, code, e, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "o", "update.json")); err == nil {
		t.Error("un manifeste a été écrit malgré les refus")
	}
}

func TestSignOtherChannelAndMinSeq(t *testing.T) {
	dir, key := setup(t)
	bin := filepath.Join(dir, "b")
	os.WriteFile(bin, armBinary(9, true), 0o755)
	if code, _, e := oq(t, "sign", "-key", key, "-in", bin, "-version", "v", "-seq", "9", "-min-seq", "4", "-channel", "beta", "-asset", "oq-beta.bin", "-outdir", dir+"/r"); code != 0 {
		t.Fatalf("%s", e)
	}
	mb, err := os.ReadFile(dir + "/r/update-beta.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sigupdate.ParseManifest(mb)
	if err != nil || m.Channel != "beta" || m.MinSeq != 4 || m.Asset != "oq-beta.bin" {
		t.Errorf("%+v %v", m, err)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := oq(t); code != 2 {
		t.Error("sans argument")
	}
	if code, _, e := oq(t, "truc"); code != 2 || !strings.Contains(e, "inconnue") {
		t.Errorf("commande inconnue : %d %s", code, e)
	}
	if code, out, _ := oq(t, "help"); code != 0 || !strings.Contains(out, "keygen") {
		t.Error("aide")
	}
}
