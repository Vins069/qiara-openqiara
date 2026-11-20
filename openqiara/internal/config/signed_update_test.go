package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Une configuration sans mise à jour signée ne change pas de forme sur le disque (anciens fichiers,
// retour arrière vers une version sans cette option) ; réglée, elle fait l'aller-retour.
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
	if s.Get().SignedUpdate.Enabled {
		t.Fatal("mise à jour signée active par défaut")
	}
	want := SignedUpdateConfig{Enabled: true, Repo: "a/b", Channel: "beta", IntervalHours: 24}
	if err := s.Update(func(c *Config) { c.SignedUpdate = want }); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().SignedUpdate; got != want {
		t.Errorf("relu %+v, attendu %+v", got, want)
	}
}
