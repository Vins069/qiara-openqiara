package sigupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const mb1 = 1_100_000

func TestInstallHappyPath(t *testing.T) {
	g := newRig(t)
	bin := fakeBinary(mb1, 1)
	m := g.rel.publish(5, bin, nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if len(g.stage) != 1 || !strings.HasSuffix(g.stage[0], " "+m.SHA256) {
		t.Fatalf("dépôt chez QIARA Plus : %v", g.stage)
	}
	if g.stageSz != int64(len(bin)) {
		t.Errorf("fichier déposé de %d octets, %d attendus", g.stageSz, len(bin))
	}
	if g.restarts != 1 || g.rollbacks != 0 {
		t.Errorf("relances %d, annulations %d", g.restarts, g.rollbacks)
	}
	g.incomingEmpty(t)
	st := g.u.Status()
	if st.PendingSeq != 5 || st.SeenSeq != 5 || st.LastCheck == "" {
		t.Errorf("état %+v", st)
	}
	// l'état survit à un redémarrage du daemon
	u2 := &Updater{StatePath: g.u.StatePath, Keys: g.u.Keys, Settings: g.u.Settings}
	if s := u2.Status(); s.PendingSeq != 5 {
		t.Errorf("état perdu : %+v", s)
	}
}

func TestNothingDownloadedWhenUpToDate(t *testing.T) {
	g := newRig(t)
	g.u.RunningSeq = 5
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResUpToDate {
		t.Fatalf("résultat %q", got)
	}
	if g.rel.count("/openqiarad-test") != 0 || len(g.stage) != 0 {
		t.Error("binaire téléchargé alors qu'on est à jour")
	}
}

func TestRollbackAndReplay(t *testing.T) {
	g := newRig(t)
	g.u.RunningSeq = 5
	g.rel.publish(4, fakeBinary(mb1, 1), nil) // plus ancien que ce qui tourne
	if got := g.check(t); got != ResUpToDate {
		t.Fatalf("seq 4 sur 5 : %q", got)
	}
	// un serveur qui rejoue un vieux manifeste après qu'on en a vu un plus récent
	g.u.RunningSeq = 2
	g.rel.publish(7, fakeBinary(mb1, 1), func(m *Manifest) { m.MinSeq = 6 }) // vu, mais exige min_seq 6
	if got := g.check(t); got != ResRefused {
		t.Fatalf("min_seq : %q", got)
	}
	g.rel.publish(6, fakeBinary(mb1, 2), nil)
	if got := g.check(t); got != ResRefused || !strings.Contains(g.u.Status().Message, "retour arrière refusé") {
		t.Fatalf("seq 6 après avoir vu 7 : %q / %s", got, g.u.Status().Message)
	}
	if len(g.stage) != 0 {
		t.Error("une version plus ancienne que la dernière vue a été déposée")
	}
	// le même seq signé par la même clé mais avec un autre binaire reste un seq <= déjà installé
	g.u.RunningSeq = 7
	g.rel.publish(7, fakeBinary(mb1, 3), nil)
	if got := g.check(t); got != ResUpToDate {
		t.Fatalf("seq 7 sur 7 : %q", got)
	}
}

func TestMinSeq(t *testing.T) {
	g := newRig(t)
	g.u.RunningSeq = 0
	g.rel.publish(9, fakeBinary(mb1, 1), func(m *Manifest) { m.MinSeq = 4 })
	if got := g.check(t); got != ResRefused || len(g.stage) != 0 {
		t.Fatalf("résultat %q, dépôts %v", got, g.stage)
	}
	g.u.RunningSeq = 4
	g.u.chk.Lock()
	g.u.st.HighSeq = 0
	g.u.chk.Unlock()
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("à partir de min_seq : %q", got)
	}
}

func TestChannelMismatch(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.settings.Channel = "beta"
	if got := g.check(t); got != ResNetwork { // update-beta.json n'existe pas : 404
		t.Fatalf("canal sans manifeste : %q", got)
	}
	// un manifeste du canal « stable » placé sous le nom du canal « beta »
	g.rel.files["/update-beta.json"] = g.rel.files["/update.json"]
	g.rel.files["/update-beta.json.sig"] = g.rel.files["/update.json.sig"]
	if got := g.check(t); got != ResRefused || len(g.stage) != 0 {
		t.Fatalf("canal différent : %q", got)
	}
}

func TestFailedVersionNotRetried(t *testing.T) {
	g := newRig(t)
	g.rel.publish(6, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("1re installation : %q", got)
	}
	// l'essai est fini sans que la version tourne : QIARA Plus est revenu en arrière
	g.trial.Store(false)
	g.u.RunningSeq = 5
	n := len(g.stage)
	if got := g.check(t); got != ResRefused {
		t.Fatalf("réessai de la version en échec : %q", got)
	}
	if s := g.u.Status(); s.FailedSeq != 6 || s.PendingSeq != 0 {
		t.Fatalf("retour arrière non constaté : %+v", s)
	}
	if len(g.stage) != n || g.rel.count("/openqiarad-test") != 1 {
		t.Error("la version revenue en arrière a été retéléchargée ou redéposée")
	}
	g.rel.publish(7, fakeBinary(mb1, 4), nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("version suivante : %q", got)
	}
}

func TestReconcile(t *testing.T) {
	g := newRig(t)
	g.u.chk.Lock()
	g.u.load()
	g.u.st.PendingSeq = 6
	g.u.chk.Unlock()

	g.trial.Store(true)
	g.u.RunningSeq = 6
	g.u.chk.Lock()
	g.u.reconcile()
	g.u.chk.Unlock()
	if s := g.u.Status(); s.Result != ResInstalling || s.PendingSeq != 6 {
		t.Errorf("pendant l'essai : %+v", s)
	}
	g.trial.Store(false)
	g.u.chk.Lock()
	g.u.reconcile()
	g.u.chk.Unlock()
	if s := g.u.Status(); s.Result != ResInstalled || s.PendingSeq != 0 || s.FailedSeq != 0 {
		t.Errorf("après validation : %+v", s)
	}
}

func TestArmedAlarmDoesNothing(t *testing.T) {
	g := newRig(t)
	g.armed.Store(true)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting {
		t.Fatalf("résultat %q", got)
	}
	if g.rel.count("/openqiarad-test") != 0 || len(g.stage) != 0 || g.restarts != 0 {
		t.Error("geste fait alarme armée")
	}
	// état d'alarme inconnu = non désarmé : la fonction Disarmed le dit déjà, on vérifie aussi l'absence de fonction
	g.u.Disarmed = nil
	if got := g.check(t); got != ResWaiting || len(g.stage) != 0 {
		t.Fatalf("sans état d'alarme : %q", got)
	}
}

func TestAlarmArmedDuringDownload(t *testing.T) {
	g := newRig(t)
	bin := fakeBinary(mb1, 1)
	g.rel.publish(5, bin, nil)
	g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.rel.mu.Lock()
		b := g.rel.files[r.URL.Path]
		g.rel.mu.Unlock()
		if r.URL.Path == "/openqiarad-test" {
			g.armed.Store(true) // l'alarme est armée pendant le téléchargement
		}
		w.Write(b)
	})
	if got := g.check(t); got != ResWaiting || len(g.stage) != 0 || g.restarts != 0 {
		t.Fatalf("résultat %q, dépôts %v, relances %d", got, g.stage, g.restarts)
	}
	g.incomingEmpty(t)
}

func TestAlarmArmedAfterStageRollsBack(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	calls := 0
	g.u.Disarmed = func() bool { calls++; return calls < 3 } // désarmée aux 2 premiers contrôles, armée au 3e (après le dépôt)
	if got := g.check(t); got != ResWaiting {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if g.rollbacks != 1 || g.restarts != 0 || g.u.Status().PendingSeq != 0 {
		t.Errorf("annulations %d, relances %d, état %+v", g.rollbacks, g.restarts, g.u.Status())
	}
}

func TestTrialInProgress(t *testing.T) {
	g := newRig(t)
	g.trial.Store(true)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != 0 || len(g.stage) != 0 {
		t.Fatalf("résultat %q", got)
	}
}

func TestQplusNotReady(t *testing.T) {
	g := newRig(t)
	g.readyErr = errTest
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != 0 {
		t.Fatalf("résultat %q", got)
	}
}

func TestNotEnoughSpace(t *testing.T) {
	g := newRig(t)
	g.u.FreeSpace = func(string) (uint64, error) { return 2 << 20, nil }
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != 0 {
		t.Fatalf("résultat %q", got)
	}
}

func TestRestartFailureUndoesTrial(t *testing.T) {
	g := newRig(t)
	g.restartErr = errTest
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting {
		t.Fatalf("résultat %q", got)
	}
	if g.rollbacks != 1 || g.u.Status().PendingSeq != 0 || g.u.Status().FailedSeq != 0 {
		t.Errorf("annulations %d, état %+v", g.rollbacks, g.u.Status())
	}
}

func TestStageRefusal(t *testing.T) {
	g := newRig(t)
	g.stageErr = errTest
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResError || g.restarts != 0 || g.u.Status().PendingSeq != 0 {
		t.Fatalf("résultat %q", got)
	}
	g.incomingEmpty(t)
}

func TestWrongSHA(t *testing.T) {
	g := newRig(t)
	bin := fakeBinary(mb1, 1)
	g.rel.publish(5, bin, nil)
	other := fakeBinary(mb1, 9) // même taille, autre contenu : signé pour bin, servi autre
	g.rel.files["/openqiarad-test"] = other
	if got := g.check(t); got != ResNetwork || !strings.Contains(g.u.Status().Message, "SHA-256") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if len(g.stage) != 0 || g.restarts != 0 {
		t.Error("binaire à somme fausse déposé")
	}
	g.incomingEmpty(t)
}

func TestSizeMismatchAndExcess(t *testing.T) {
	bin := fakeBinary(mb1, 1)
	for name, served := range map[string][]byte{
		"plus court": bin[:len(bin)-10],
		"plus long":  append(append([]byte(nil), bin...), make([]byte, 5000)...),
		"énorme":     make([]byte, 30_000_000),
	} {
		g := newRig(t)
		g.rel.publish(5, bin, nil)
		g.rel.files["/openqiarad-test"] = served
		if got := g.check(t); got != ResNetwork || len(g.stage) != 0 {
			t.Errorf("%s : résultat %q", name, got)
		}
		g.incomingEmpty(t)
	}
}

func TestOversizedManifestAndSignature(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.rel.files["/update.json"] = make([]byte, 100_000)
	if got := g.check(t); got != ResNetwork {
		t.Errorf("manifeste énorme : %q", got)
	}
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.rel.files["/update.json.sig"] = make([]byte, 100_000)
	if got := g.check(t); got != ResNetwork {
		t.Errorf("signature énorme : %q", got)
	}
}

func TestSignatureFailureStopsEverything(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.rel.files["/update.json"] = []byte(strings.Replace(string(g.rel.files["/update.json"]), `"seq": 5`, `"seq": 6`, 1))
	if got := g.check(t); got != ResSignature {
		t.Fatalf("résultat %q", got)
	}
	if g.rel.count("/openqiarad-test") != 0 || len(g.stage) != 0 {
		t.Error("téléchargement malgré une signature fausse")
	}
	// signé par une autre clé
	g2 := newRig(t)
	other := newRelease(t)
	g2.u.Keys = other.pub2()
	g2.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g2.check(t); got != ResSignature || len(g2.stage) != 0 {
		t.Fatalf("clé inconnue : %q", got)
	}
}

func TestNoKeyDisablesEverything(t *testing.T) {
	g := newRig(t)
	g.u.Keys = nil
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResNoKey {
		t.Fatalf("résultat %q", got)
	}
	if n := len(g.rel.hits); n != 0 {
		t.Errorf("%d requête(s) réseau sans clé", n)
	}
	if g.u.Status().Active {
		t.Error("« active » sans clé")
	}
}

func TestDisabledDoesNothing(t *testing.T) {
	g := newRig(t)
	g.settings.Enabled = false
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResDisabled || len(g.rel.hits) != 0 {
		t.Fatalf("résultat %q, requêtes %v", got, g.rel.hits)
	}
}

func TestBadSettings(t *testing.T) {
	g := newRig(t)
	for _, r := range []string{"", "pasdeslash", "a/b/c", "../x", "a b/c", "o/"} {
		g.settings.Repo = r
		if got := g.check(t); got != ResError || len(g.rel.hits) != 0 {
			t.Errorf("dépôt %q : résultat %q", r, got)
		}
	}
	g.settings.Repo = "Vins069/qiara-openqiara"
	g.settings.Channel = "../x"
	if got := g.check(t); got != ResError {
		t.Errorf("canal invalide : %q", got)
	}
}

func TestSlowServer(t *testing.T) {
	g := newRig(t)
	g.u.DownloadTimeout = 300 * time.Millisecond
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.rel.mu.Lock()
		b := g.rel.files[r.URL.Path]
		g.rel.mu.Unlock()
		if r.URL.Path == "/openqiarad-test" {
			w.Write(b[:1000])
			w.(http.Flusher).Flush()
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		w.Write(b)
	})
	start := time.Now()
	if got := g.check(t); got != ResNetwork {
		t.Fatalf("résultat %q", got)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("le délai n'a pas coupé le téléchargement (%v)", time.Since(start))
	}
	if len(g.stage) != 0 {
		t.Error("dépôt d'un fichier incomplet")
	}
	g.incomingEmpty(t)
}

func TestSlowManifest(t *testing.T) {
	g := newRig(t)
	g.u.ManifestTimeout = 200 * time.Millisecond
	g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	if got := g.check(t); got != ResNetwork {
		t.Fatalf("résultat %q", got)
	}
}

func TestConnectionCut(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.rel.mu.Lock()
		b := g.rel.files[r.URL.Path]
		g.rel.mu.Unlock()
		if r.URL.Path == "/openqiarad-test" {
			w.Header().Set("Content-Length", "1100000")
			w.Write(b[:50_000])
			w.(http.Flusher).Flush()
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close() // coupure en cours de route
			return
		}
		w.Write(b)
	})
	if got := g.check(t); got != ResNetwork || len(g.stage) != 0 {
		t.Fatalf("résultat %q", got)
	}
	g.incomingEmpty(t)
}

func TestHTTPErrors(t *testing.T) {
	for _, code := range []int{403, 404, 429, 500, 503} {
		g := newRig(t)
		g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "non", code) })
		if got := g.check(t); got != ResNetwork {
			t.Errorf("HTTP %d : %q", code, got)
		}
	}
}

func TestRedirectFollowedOverTLS(t *testing.T) {
	rel := newRelease(t)
	bin := fakeBinary(mb1, 1)
	rel.publish(5, bin, nil)
	// « latest/download/x » redirige vers « objets/x », comme GitHub vers son CDN
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/latest/") {
			http.Redirect(w, r, "/objets/"+strings.TrimPrefix(r.URL.Path, "/latest/"), http.StatusFound)
			return
		}
		r.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, "/objets/")
		rel.ServeHTTP(w, r)
	}))
	defer tls.Close()
	g := newRig(t)
	g.u.Keys = rel.pub2()
	c := tls.Client()
	c.CheckRedirect = RedirectPolicy(func(h string) bool { return h == "127.0.0.1" })
	g.u.HTTP = c
	g.u.BaseURL = func(string) string { return tls.URL + "/latest/" }
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
}

func TestRedirectToPlainHTTPRefused(t *testing.T) {
	g := newRig(t)
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/update.json", http.StatusFound)
	})
	g.u.HTTP = &http.Client{CheckRedirect: RedirectPolicy(func(string) bool { return true })}
	if got := g.check(t); got != ResNetwork || !strings.Contains(g.u.Status().Message, "HTTPS") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
}

func TestRedirectPolicy(t *testing.T) {
	p := RedirectPolicy(GitHubHost)
	req := func(raw string) *http.Request { u, _ := url.Parse(raw); return &http.Request{URL: u} }
	for _, ok := range []string{"https://github.com/x", "https://objects.githubusercontent.com/y", "https://release-assets.githubusercontent.com/z"} {
		if err := p(req(ok), nil); err != nil {
			t.Errorf("%s refusé : %v", ok, err)
		}
	}
	for _, bad := range []string{"https://evil.example/x", "http://github.com/x", "https://github.com.evil.example/x", "https://evilgithub.com/x", "https://notgithubusercontent.com/x"} {
		if err := p(req(bad), nil); err == nil {
			t.Errorf("%s accepté", bad)
		}
	}
	if err := p(req("https://github.com/x"), make([]*http.Request, 5)); err == nil {
		t.Error("6e redirection acceptée")
	}
}

func TestClampInterval(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{0: DefaultInterval, -time.Hour: DefaultInterval, time.Minute: MinInterval,
		6 * time.Hour: 6 * time.Hour, 24 * time.Hour: 24 * time.Hour, 1000 * time.Hour: MaxInterval} {
		if got := ClampInterval(in); got != want {
			t.Errorf("%v -> %v, %v attendu", in, got, want)
		}
	}
}

func TestStateCorruptFile(t *testing.T) {
	g := newRig(t)
	os.MkdirAll(g.dir+"/qp", 0o755)
	os.WriteFile(g.u.StatePath, []byte("{pas du json"), 0o644)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("résultat %q", got)
	}
}

func TestStaleIncomingCleaned(t *testing.T) {
	g := newRig(t)
	os.MkdirAll(g.u.IncomingDir, 0o755)
	os.WriteFile(g.u.IncomingDir+"/sig-3.bin.part", []byte("reste"), 0o600)
	os.WriteFile(g.u.IncomingDir+"/autre.txt", []byte("pas à nous"), 0o600)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.check(t)
	ents, _ := os.ReadDir(g.u.IncomingDir)
	if len(ents) != 1 || ents[0].Name() != "autre.txt" {
		t.Errorf("contenu du dépôt : %v", ents)
	}
}

func TestPendingBlocksNewInstall(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.check(t)
	g.trial.Store(true)
	g.rel.publish(6, fakeBinary(mb1, 2), nil)
	n := g.rel.count("/openqiarad-test")
	if got := g.check(t); got != ResInstalling || g.rel.count("/openqiarad-test") != n || len(g.stage) != 1 {
		t.Fatalf("résultat %q", got)
	}
}

func TestRunChecksOnceAndHonoursEnabled(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.u.First = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.u.Run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for g.rel.count("/update.json") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if g.rel.count("/update.json") != 1 {
		t.Fatalf("%d vérification(s)", g.rel.count("/update.json"))
	}
	// désactivée : la boucle ne lit rien
	g2 := newRig(t)
	g2.settings.Enabled = false
	g2.u.First = 20 * time.Millisecond
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	g2.u.Run(ctx2)
	if len(g2.rel.hits) != 0 {
		t.Error("requête réseau alors que l'option est désactivée")
	}
}

func TestSumOfBinaryMatchesSigned(t *testing.T) {
	// le dépôt remis à QIARA Plus a bien la somme annoncée
	g := newRig(t)
	bin := fakeBinary(mb1, 1)
	g.rel.publish(5, bin, nil)
	var seen string
	g.u.Stage = func(_ context.Context, f, sha string) error {
		b, _ := os.ReadFile(f)
		h := sha256.Sum256(b)
		seen = hex.EncodeToString(h[:])
		if seen != sha {
			t.Errorf("somme du fichier %s, annoncée %s", seen, sha)
		}
		return nil
	}
	g.check(t)
	if seen == "" {
		t.Fatal("rien déposé")
	}
}

// Status ne bloque pas pendant un téléchargement (l'API et le diagnostic MQTT l'appellent).
func TestStatusNotBlockedDuringDownload(t *testing.T) {
	g := newRig(t)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	inDownload, release := make(chan struct{}), make(chan struct{})
	g.rel.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.rel.mu.Lock()
		b := g.rel.files[r.URL.Path]
		g.rel.mu.Unlock()
		if r.URL.Path == "/openqiarad-test" {
			close(inDownload)
			<-release
		}
		w.Write(b)
	})
	done := make(chan string)
	go func() { done <- g.check(t) }()
	<-inDownload
	got := make(chan Status)
	go func() { got <- g.u.Status() }()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("Status bloqué pendant le téléchargement")
	}
	close(release)
	if r := <-done; r != ResInstalling {
		t.Errorf("résultat %q", r)
	}
	if s := g.u.Status(); s.PendingSeq != 5 || s.Result != ResInstalling {
		t.Errorf("état publié : %+v", s)
	}
}
