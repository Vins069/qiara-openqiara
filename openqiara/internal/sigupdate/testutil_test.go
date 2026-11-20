package sigupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBinary fabrique un « binaire » de n octets (l'ELF est vérifié par qp-update, pas ici).
func fakeBinary(n int, fill byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill + byte(i%7)
	}
	copy(b, []byte{0x7f, 'E', 'L', 'F', 1, 1, 1, 0})
	return b
}

type release struct {
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
	files   map[string][]byte // chemin -> contenu
	hits    map[string]int
	mu      sync.Mutex
	handler http.Handler // surcharge facultative
}

func newRelease(t *testing.T) *release {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &release{pub: pub, priv: priv, files: map[string][]byte{}, hits: map[string]int{}}
}

func (r *release) sign(m Manifest) (manifest, sig []byte) {
	manifest, _ = json.MarshalIndent(m, "", " ")
	return manifest, EncodeSignature(ed25519.Sign(r.priv, SignedBytes(manifest)))
}

// publish dépose manifeste, signature et binaire pour la graine donnée.
func (r *release) publish(seq int64, bin []byte, mod func(*Manifest)) Manifest {
	sum := sha256.Sum256(bin)
	m := Manifest{Channel: "stable", Seq: seq, Version: "v-test-" + hex.EncodeToString([]byte{byte(seq)}), IssuedAt: "2026-11-20T10:00:00Z",
		Asset: "openqiarad-test", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}
	if mod != nil {
		mod(&m)
	}
	mb, sb := r.sign(m)
	r.files["/"+ManifestName(m.Channel)] = mb
	r.files["/"+ManifestName(m.Channel)+".sig"] = sb
	r.files["/"+m.Asset] = bin
	return m
}

func (r *release) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.hits[req.URL.Path]++
	h := r.handler
	b, ok := r.files[req.URL.Path]
	r.mu.Unlock()
	if h != nil {
		h.ServeHTTP(w, req)
		return
	}
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Write(b)
}

func (r *release) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

// rig : un Updater complet sur des faux.
type rig struct {
	u          *Updater
	rel        *release
	srv        *httptest.Server
	dir        string
	settings   Settings
	armed      atomic.Bool // vrai = alarme armée
	trial      atomic.Bool
	readyErr   error
	stage      []string // « fichier sha »
	stageErr   error
	stageSz    int64 // taille du fichier vu au dépôt
	restarts   int
	restartErr error
	rollbacks  int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	rel := newRelease(t)
	srv := httptest.NewServer(rel)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	g := &rig{rel: rel, srv: srv, dir: dir, settings: Settings{Enabled: true, Repo: "Vins069/qiara-openqiara"}}
	g.u = &Updater{
		Keys:        []ed25519.PublicKey{rel.pub},
		HTTP:        srv.Client(),
		BaseURL:     func(string) string { return srv.URL + "/" },
		StatePath:   filepath.Join(dir, "qp", "sigupdate.json"),
		IncomingDir: filepath.Join(dir, "incoming"),
		Settings:    func() Settings { return g.settings },
		Disarmed:    func() bool { return !g.armed.Load() },
		Ready:       func() error { return g.readyErr },
		Trial:       func() bool { return g.trial.Load() },
		Stage: func(_ context.Context, file, sha string) error {
			if st, err := os.Stat(file); err == nil {
				g.stageSz = st.Size()
			}
			g.stage = append(g.stage, file+" "+sha)
			return g.stageErr
		},
		Restart:         func() error { g.restarts++; return g.restartErr },
		Rollback:        func(context.Context) error { g.rollbacks++; return nil },
		FreeSpace:       func(string) (uint64, error) { return 1 << 30, nil },
		Now:             func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
		ManifestTimeout: 2 * time.Second,
		DownloadTimeout: 2 * time.Second,
	}
	return g
}

func (g *rig) check(t *testing.T) string {
	t.Helper()
	return g.u.CheckOnce(context.Background())
}

func (g *rig) incomingEmpty(t *testing.T) {
	t.Helper()
	ents, _ := os.ReadDir(g.u.IncomingDir)
	if len(ents) != 0 {
		t.Errorf("dépôt non vidé : %d fichier(s)", len(ents))
	}
}

var errTest = errors.New("erreur de test")

func (r *release) pub2() []ed25519.PublicKey { return []ed25519.PublicKey{r.pub} }
