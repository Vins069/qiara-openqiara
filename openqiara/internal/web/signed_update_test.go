package web

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/caligone/openqiara/internal/config"
	"github.com/caligone/openqiara/internal/sigupdate"
)

func signedServer(t *testing.T, withKey, qplus, attach bool) (*Server, *config.Store, http.Handler) {
	t.Helper()
	installed := qplusInstalled
	qplusInstalled = func() bool { return qplus }
	t.Cleanup(func() { qplusInstalled = installed })
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	staticFS := fstest.MapFS{"static/index.html": &fstest.MapFile{Data: []byte("<html>ui</html>")}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(&stubCamera{}, store, func() bool { return false }, &MQTTCallbacks{}, staticFS, logger)
	if attach {
		u := &sigupdate.Updater{StatePath: filepath.Join(t.TempDir(), "s.json"),
			Settings: func() sigupdate.Settings {
				c := store.Get().SignedUpdate
				return sigupdate.Settings{Enabled: c.IsEnabled(), Repo: c.EffectiveRepo()}
			}}
		if withKey {
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			u.Keys = []ed25519.PublicKey{pub}
		}
		s.SetSignedUpdate(u)
	}
	mux, err := s.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	return s, store, s.basicAuth(mux)
}

func call(h http.Handler, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/update/signed", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSignedUpdateAPINotWired(t *testing.T) {
	_, _, h := signedServer(t, false, false, false)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if rec := call(h, m, `{"enabled":false}`); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s : code %d", m, rec.Code)
		}
	}
}

func TestSignedUpdateAPIDefaults(t *testing.T) {
	_, _, h := signedServer(t, false, true, true)
	rec := call(h, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d : %s", rec.Code, rec.Body.String())
	}
	var v signedUpdateView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	// réglages de la distribution : activée, dépôt de Vincent, 12 h ; inerte ici (aucune clé gravée)
	if !v.Enabled || v.Repo != "Vins069/qiara-openqiara" || v.Available || v.Channel != "stable" ||
		v.IntervalHours != 12 || v.Status.Keys != 0 || v.Status.Active {
		t.Errorf("valeurs par défaut inattendues : %+v", v)
	}
}

func TestSignedUpdateAPIEnableRules(t *testing.T) {
	type tc struct {
		name           string
		key, qplus     bool
		body           string
		want           int
		wantEnabledNow bool
	}
	for _, c := range []tc{
		// refusées, les demandes laissent le réglage par défaut (activée) inchangé
		{"sans clé gravée : refusé", false, true, `{"enabled":true,"repo":"Vins069/x"}`, http.StatusConflict, true},
		{"sans QIARA Plus : refusé", true, false, `{"enabled":true,"repo":"Vins069/x"}`, http.StatusConflict, true},
		{"sans dépôt : celui de la distribution", true, true, `{"enabled":true}`, http.StatusOK, true},
		{"tout est là : activée", true, true, `{"enabled":true,"repo":"Vins069/x","interval_hours":24,"channel":"beta"}`, http.StatusOK, true},
		{"couper reste possible sans clé", false, false, `{"enabled":false}`, http.StatusOK, false},
		{"régler sans clé (activée par défaut, inerte)", false, true, `{"repo":"Vins069/x"}`, http.StatusOK, true},
		{"régler l'intervalle sans clé", false, false, `{"interval_hours":24}`, http.StatusOK, true},
	} {
		_, store, h := signedServer(t, c.key, c.qplus, true)
		rec := call(h, http.MethodPut, c.body)
		if rec.Code != c.want {
			t.Errorf("%s : code %d (%s), attendu %d", c.name, rec.Code, rec.Body.String(), c.want)
			continue
		}
		if got := store.Get().SignedUpdate.IsEnabled(); got != c.wantEnabledNow {
			t.Errorf("%s : enabled enregistré = %v", c.name, got)
		}
	}
}

func TestSignedUpdateAPIValidationAndPersistence(t *testing.T) {
	_, store, h := signedServer(t, true, true, true)
	for _, body := range []string{
		``, `{}`, `pas du json`, `{"inconnu":1}`,
		`{"repo":"pas de slash"}`, `{"repo":"a/b/c"}`, `{"repo":"../x"}`,
		`{"channel":"Stable"}`, `{"channel":"a b"}`, `{"channel":"x/y"}`,
		`{"interval_hours":1}`, `{"interval_hours":5}`, `{"interval_hours":169}`, `{"interval_hours":-6}`,
	} {
		if rec := call(h, http.MethodPut, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q : code %d, 400 attendu", body, rec.Code)
		}
	}
	if store.Get().SignedUpdate != (config.SignedUpdateConfig{}) {
		t.Errorf("réglages modifiés malgré des refus : %+v", store.Get().SignedUpdate)
	}
	if rec := call(h, http.MethodPut, `{"enabled":true,"repo":"Vins069/qiara-openqiara","interval_hours":6}`); rec.Code != http.StatusOK {
		t.Fatalf("code %d : %s", rec.Code, rec.Body.String())
	}
	// un second PUT partiel ne remet pas le reste à zéro
	if rec := call(h, http.MethodPut, `{"interval_hours":48}`); rec.Code != http.StatusOK {
		t.Fatalf("code %d : %s", rec.Code, rec.Body.String())
	}
	got := store.Get().SignedUpdate
	if !got.IsEnabled() || got.Enabled == nil || got.Repo != "Vins069/qiara-openqiara" || got.IntervalHours != 48 {
		t.Errorf("réglages %+v", got)
	}
	// la lecture renvoie l'état
	var v signedUpdateView
	json.Unmarshal(call(h, http.MethodGet, "").Body.Bytes(), &v)
	if !v.Enabled || !v.Available || v.Status.Keys != 1 || v.IntervalHours != 48 {
		t.Errorf("lecture : %+v", v)
	}
}

// L'installation automatique de l'amont reste refusée pour une version locale, et la route de la
// mise à jour signée n'y change rien.
func TestUpstreamAutoStillRefusedWithSignedAPI(t *testing.T) {
	s, store, h := signedServer(t, true, true, true)
	s.SetVersion("v0.1.0-rc.25-claude-v17 (claude-v17, 2026-11-20)")
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/update", strings.NewReader(`{"auto":true}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || store.Get().Update.Auto {
		t.Fatalf("code %d, auto=%v", rec.Code, store.Get().Update.Auto)
	}
	// et la configuration complète n'a pas changé de forme
	req = httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "signed") {
		t.Errorf("GET /config expose la mise à jour signée : %s", rec.Body.String())
	}
}

// Revue V17 : main attache la mise à jour signée après le démarrage du serveur web ; une requête
// arrivée à ce moment-là ne doit pas lire le champ pendant son écriture (course signalée par -race
// sur claude17).
func TestSignedUpdateAttachWhileServing(t *testing.T) {
	s, _, h := signedServer(t, false, false, false)
	u := &sigupdate.Updater{StatePath: filepath.Join(t.TempDir(), "s.json")}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if rec := call(h, http.MethodGet, ""); rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusOK {
				t.Errorf("code %d", rec.Code)
				return
			}
		}
	}()
	s.SetSignedUpdate(u)
	<-done
	if rec := call(h, http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Errorf("après rattachement : code %d", rec.Code)
	}
}

// Couper, puis remettre le dépôt de la distribution par un repo vide : les deux se lisent ensuite.
func TestSignedUpdateAPIDisableAndDefaultRepo(t *testing.T) {
	_, store, h := signedServer(t, false, true, true)
	if rec := call(h, http.MethodPut, `{"enabled":false,"repo":"a/b"}`); rec.Code != http.StatusOK {
		t.Fatalf("code %d : %s", rec.Code, rec.Body.String())
	}
	if store.Get().SignedUpdate.IsEnabled() {
		t.Fatal("coupure non prise en compte")
	}
	if rec := call(h, http.MethodPut, `{"repo":""}`); rec.Code != http.StatusOK {
		t.Fatalf("code %d : %s", rec.Code, rec.Body.String())
	}
	var v signedUpdateView
	json.Unmarshal(call(h, http.MethodGet, "").Body.Bytes(), &v)
	if v.Enabled || v.Repo != "Vins069/qiara-openqiara" {
		t.Errorf("lecture : %+v", v)
	}
}
