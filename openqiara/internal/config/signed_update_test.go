package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reload(t *testing.T, path string) SignedUpdateConfig {
	t.Helper()
	s := NewStore(path)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	return s.Get().SignedUpdate
}

// Une configuration sans mise à jour signée ne change pas de forme sur le disque (anciens fichiers,
// retour arrière vers une version sans cette option) et prend les réglages de la distribution :
// activée, dépôt de la distribution, 12 h. Réglée, elle fait l'aller-retour.
func TestSignedUpdateConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewStore(path)
	if err := s.Update(func(c *Config) { c.Update.Auto = false }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "signed_update") {
		t.Errorf("signed_update écrit alors qu'il est vide : %s", b)
	}
	d := reload(t, path)
	if !d.IsEnabled() || d.EffectiveRepo() != "Vins069/qiara-openqiara" || d.IntervalHours != 0 {
		t.Fatalf("réglages par défaut : activée %v, dépôt %q, %d h", d.IsEnabled(), d.EffectiveRepo(), d.IntervalHours)
	}
	on := true
	want := SignedUpdateConfig{Enabled: &on, Repo: "a/b", Channel: "beta", IntervalHours: 24}
	if err := s.Update(func(c *Config) { c.SignedUpdate = want }); err != nil {
		t.Fatal(err)
	}
	got := reload(t, path)
	if !got.IsEnabled() || got.Repo != "a/b" || got.Channel != "beta" || got.IntervalHours != 24 {
		t.Errorf("relu %+v, attendu %+v", got, want)
	}
}

// Une coupure voulue est écrite et survit au redémarrage, même sans autre réglage : sinon le fichier
// redeviendrait vide et la fonction se rallumerait seule.
func TestSignedUpdateDisabledPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewStore(path)
	off := false
	if err := s.Update(func(c *Config) { c.SignedUpdate = SignedUpdateConfig{Enabled: &off} }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"enabled": false`) {
		t.Errorf("coupure non écrite : %s", b)
	}
	if reload(t, path).IsEnabled() {
		t.Fatal("la mise à jour signée se rallume au redémarrage")
	}
}

// Un ancien fichier V17 d'essai, avec enabled écrit en clair, est relu tel quel.
func TestSignedUpdateExplicitValues(t *testing.T) {
	for body, want := range map[string]bool{
		`{"signed_update":{"enabled":false,"repo":"x/y"}}`: false,
		`{"signed_update":{"enabled":true}}`:               true,
		`{"signed_update":{"repo":"x/y"}}`:                 true,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := reload(t, path).IsEnabled(); got != want {
			t.Errorf("%s : activée %v, attendu %v", body, got, want)
		}
	}
}
