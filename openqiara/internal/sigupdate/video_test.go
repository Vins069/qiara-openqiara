package sigupdate

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Contrôle du flux vidéo (décision du 21/11/2026) : avant la relance, et par la version installée après.

type videoRig struct {
	*rig
	closed   atomic.Bool  // cache fermé
	flowing  atomic.Bool  // le flux coule
	probes   atomic.Int32 // écoutes du flux
	usePrevs atomic.Int32
	rbs      atomic.Int32
	before   int32 // écoutes avant la relance (installed)
}

func newVideoRig(t *testing.T) *videoRig {
	t.Helper()
	v := &videoRig{rig: newRig(t)}
	v.flowing.Store(true)
	v.u.ShutterOpen = func() bool { return !v.closed.Load() }
	v.u.VideoProbe = func(ctx context.Context, wait time.Duration) error {
		v.probes.Add(1)
		if v.flowing.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		return errTest
	}
	v.u.UsePrev = func(context.Context) error { v.usePrevs.Add(1); return nil }
	v.u.Rollback = func(context.Context) error { v.rbs.Add(1); return nil }
	v.u.VideoPreWait, v.u.VideoWindow = 20*time.Millisecond, 20*time.Millisecond
	v.u.VideoDeadline, v.u.VideoPoll = 100*time.Millisecond, 10*time.Millisecond
	v.u.First = time.Hour // Run : seulement le rapprochement et le contrôle du flux
	return v
}

// start lance le daemon sur le binaire seq (état relu du disque), comme au démarrage après la relance.
func (v *videoRig) start(t *testing.T, seq int64) {
	t.Helper()
	v.u.chk.Lock()
	v.u.RunningSeq, v.u.loaded = seq, false
	v.u.chk.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { v.u.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("délai dépassé : %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// installed : la version 6 est déposée chez QIARA Plus et la relance demandée, depuis la 5.
func (v *videoRig) installed(t *testing.T) {
	t.Helper()
	v.u.RunningSeq = 5
	v.rel.publish(6, fakeBinary(mb1, 1), nil)
	if got := v.check(t); got != ResInstalling {
		t.Fatalf("installation de 6 : %q (%s)", got, v.u.Status().Message)
	}
	if st := loadState(v.u.StatePath); st.VideoSeq != 6 {
		t.Fatalf("flux de la 6 non mis à contrôler : %+v", st)
	}
	v.before = v.probes.Swap(0) // écoutes faites avant la relance, par la version qui installe
}

func TestVideoBeforeRelaunchBlocks(t *testing.T) {
	v := newVideoRig(t)
	v.u.RunningSeq = 5
	v.rel.publish(6, fakeBinary(mb1, 1), nil)
	v.flowing.Store(false)
	if got := v.check(t); got != ResWaiting || !strings.Contains(v.u.Status().Message, "flux vidéo") {
		t.Fatalf("relance sans flux vidéo : %q (%s)", got, v.u.Status().Message)
	}
	if n := v.rel.count("/openqiarad-test"); n != 0 || len(v.stage) != 0 || v.restarts != 0 {
		t.Errorf("téléchargé %d fois, %d dépôt(s), %d relance(s)", n, len(v.stage), v.restarts)
	}
	v.flowing.Store(true)
	if got := v.check(t); got != ResInstalling {
		t.Fatalf("flux revenu : %q", got)
	}
}

// Le flux disparaît pendant le téléchargement : rien n'est déposé, le fichier est effacé.
func TestVideoBeforeStageBlocks(t *testing.T) {
	v := newVideoRig(t)
	v.u.RunningSeq = 5
	v.rel.publish(6, fakeBinary(mb1, 1), nil)
	probe := v.u.VideoProbe
	v.u.VideoProbe = func(ctx context.Context, wait time.Duration) error {
		if v.probes.Load() >= 1 {
			v.flowing.Store(false)
		}
		return probe(ctx, wait)
	}
	if got := v.check(t); got != ResWaiting {
		t.Fatalf("flux perdu avant le dépôt : %q (%s)", got, v.u.Status().Message)
	}
	if len(v.stage) != 0 || v.restarts != 0 {
		t.Errorf("%d dépôt(s), %d relance(s)", len(v.stage), v.restarts)
	}
	v.incomingEmpty(t)
	if st := loadState(v.u.StatePath); st.VideoSeq != 0 || st.PendingSeq != 0 {
		t.Errorf("état : %+v", st)
	}
}

// Cache fermé : flux en pause par conception, rien à couper ni à contrôler avant la relance.
func TestVideoBeforeShutterClosed(t *testing.T) {
	v := newVideoRig(t)
	v.closed.Store(true)
	v.flowing.Store(false)
	v.installed(t)
	if v.before != 0 {
		t.Errorf("%d écoute(s) du flux cache fermé", v.before)
	}
}

// Relance non faite : l'essai est annulé et le contrôle du flux oublié.
func TestVideoSeqClearedWhenRestartFails(t *testing.T) {
	v := newVideoRig(t)
	v.u.RunningSeq = 5
	v.restartErr = errTest
	v.rel.publish(6, fakeBinary(mb1, 1), nil)
	if got := v.check(t); got != ResWaiting {
		t.Fatalf("relance impossible : %q", got)
	}
	if st := loadState(v.u.StatePath); st.VideoSeq != 0 || st.PendingSeq != 0 {
		t.Errorf("état : %+v", st)
	}
}

// Après la relance, pendant l'essai : santé retenue jusqu'au flux constaté, puis libérée.
func TestVideoConfirmedDuringTrial(t *testing.T) {
	v := newVideoRig(t)
	v.installed(t)
	release := make(chan struct{})
	var holdSeen atomic.Value
	v.u.VideoProbe = func(ctx context.Context, wait time.Duration) error {
		holdSeen.Store(v.u.HealthHold())
		<-release
		return nil
	}
	v.trial.Store(true)
	v.start(t, 6)
	waitFor(t, "écoute du flux", func() bool { return holdSeen.Load() != nil })
	if h := holdSeen.Load().(string); !strings.Contains(h, "flux vidéo") {
		t.Fatalf("santé non retenue pendant l'écoute : %q", h)
	}
	close(release)
	waitFor(t, "flux constaté", func() bool { return loadState(v.u.StatePath).VideoSeq == 0 })
	waitFor(t, "santé libérée", func() bool { return v.u.HealthHold() == "" })
	if v.rbs.Load() != 0 || v.usePrevs.Load() != 0 {
		t.Errorf("retour arrière alors que le flux coule")
	}
}

// Pas de flux pendant l'essai : retour arrière (qp-update rollback), version jamais réessayée.
func TestVideoMissingDuringTrialRollsBack(t *testing.T) {
	v := newVideoRig(t)
	v.installed(t)
	v.flowing.Store(false)
	v.trial.Store(true)
	v.start(t, 6)
	waitFor(t, "retour arrière", func() bool { return v.rbs.Load() == 1 })
	st := v.u.Status()
	if st.Result != ResRolledBack || st.FailedSeq != 6 || st.VideoSeq != 0 || st.PendingSeq != 0 ||
		!strings.Contains(st.Message, "flux vidéo") {
		t.Fatalf("état après le retour : %+v", st)
	}
	if v.usePrevs.Load() != 0 {
		t.Error("use-prev pendant un essai")
	}
	// QIARA Plus a relancé la 5 : le message est gardé, la 6 n'est pas reproposée.
	v.trial.Store(false)
	v.restartAs(5)
	if s := v.u.Status(); s.Result != ResRolledBack || !strings.Contains(s.Message, "flux vidéo") {
		t.Errorf("après la relance de la 5 : %+v", s)
	}
	if got := v.check(t); got != ResRefused {
		t.Errorf("la 6 est reproposée : %q", got)
	}
}

// Cache fermé pendant tout l'essai : pas de santé retenue (QIARA Plus valide), contrôle à l'ouverture ;
// sans flux, retour à la version précédente (use-prev), jamais réinstallée.
func TestVideoMissingAfterValidationUsesPrev(t *testing.T) {
	v := newVideoRig(t)
	v.installed(t)
	v.flowing.Store(false)
	v.closed.Store(true)
	v.trial.Store(true)
	v.start(t, 6)
	time.Sleep(50 * time.Millisecond)
	if h := v.u.HealthHold(); h != "" || v.probes.Load() != 0 {
		t.Fatalf("cache fermé : santé retenue %q, %d écoute(s)", h, v.probes.Load())
	}
	// QIARA Plus valide la 6 ; plus tard, le cache s'ouvre et le flux ne vient pas
	v.trial.Store(false)
	v.closed.Store(false)
	waitFor(t, "retour à la version précédente", func() bool { return v.usePrevs.Load() == 1 })
	if v.rbs.Load() != 0 {
		t.Error("rollback hors essai")
	}
	if s := v.u.Status(); s.Result != ResRolledBack || s.FailedSeq != 6 || !strings.Contains(s.Message, "flux vidéo") {
		t.Fatalf("état : %+v", s)
	}
	v.restartAs(5)
	if s := v.u.Status(); !strings.Contains(s.Message, "flux vidéo") {
		t.Errorf("message perdu à la relance de la 5 : %+v", s)
	}
	if got := v.check(t); got != ResRefused {
		t.Errorf("la 6 est reproposée : %q", got)
	}
}

// Flux absent mais alarme armée : aucun retour (relance) avant le désarmement.
func TestVideoMissingWaitsForDisarm(t *testing.T) {
	v := newVideoRig(t)
	v.installed(t)
	v.flowing.Store(false)
	v.trial.Store(true)
	v.armed.Store(true)
	v.start(t, 6)
	waitFor(t, "attente du désarmement", func() bool {
		s := v.u.Status()
		return s.Result == ResWaiting && strings.Contains(s.Message, "désarmée")
	})
	time.Sleep(50 * time.Millisecond)
	if v.rbs.Load() != 0 || v.usePrevs.Load() != 0 {
		t.Fatal("retour arrière alarme armée")
	}
	if h := v.u.HealthHold(); h == "" {
		t.Error("santé libérée pendant l'attente : QIARA Plus validerait une version sans flux")
	}
	v.armed.Store(false)
	waitFor(t, "retour après le désarmement", func() bool { return v.rbs.Load() == 1 })
}

// Le cache se ferme pendant l'absence de flux : le délai repart à l'ouverture suivante.
func TestVideoDeadlineRestartsAfterShutter(t *testing.T) {
	v := newVideoRig(t)
	v.installed(t)
	v.flowing.Store(false)
	v.trial.Store(false)
	v.u.VideoDeadline = 300 * time.Millisecond
	v.start(t, 6)
	time.Sleep(200 * time.Millisecond)
	v.closed.Store(true)
	time.Sleep(200 * time.Millisecond)
	v.closed.Store(false)
	time.Sleep(150 * time.Millisecond)
	if v.usePrevs.Load() != 0 {
		t.Fatal("délai compté cache fermé")
	}
	waitFor(t, "retour", func() bool { return v.usePrevs.Load() == 1 })
}

// Une autre version tourne (retour arrière, installation à la main) : le contrôle en attente est oublié.
func TestVideoSeqForgottenForOtherVersion(t *testing.T) {
	v := newVideoRig(t)
	v.installed(t)
	v.trial.Store(false)
	v.restartAs(5) // QIARA Plus est revenu à la 5
	if st := loadState(v.u.StatePath); st.VideoSeq != 0 {
		t.Errorf("contrôle du flux de la 6 gardé alors que la 5 tourne : %+v", st)
	}
	v.flowing.Store(false)
	v.start(t, 5)
	time.Sleep(50 * time.Millisecond)
	if v.probes.Load() != 0 || v.rbs.Load() != 0 || v.usePrevs.Load() != 0 {
		t.Error("contrôle lancé pour une version qui n'est pas à contrôler")
	}
}

// Sans sonde (VideoProbe nil), rien ne change : ni contrôle avant, ni après, ni santé retenue.
func TestVideoProbeAbsent(t *testing.T) {
	v := newVideoRig(t)
	v.u.VideoProbe = nil
	v.installed(t)
	v.trial.Store(true)
	v.start(t, 6)
	time.Sleep(30 * time.Millisecond)
	if h := v.u.HealthHold(); h != "" || v.rbs.Load() != 0 {
		t.Errorf("sans sonde : santé %q, %d retour(s)", h, v.rbs.Load())
	}
}
