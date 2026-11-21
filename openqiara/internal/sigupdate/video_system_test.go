//go:build linux

package sigupdate

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Contrôle du flux avec le vrai qp-update : la version installée sans flux vidéo fait revenir QIARA Plus
// à la version précédente, pendant l'essai (rollback) comme après la validation (use-prev).

func (b *qpBench) videoFailing(t *testing.T, seq int64) {
	t.Helper()
	g := b.g
	g.u.VideoProbe = func(ctx context.Context, wait time.Duration) error { return errTest }
	g.u.VideoWindow, g.u.VideoDeadline, g.u.VideoPoll = 10*time.Millisecond, 50*time.Millisecond, 10*time.Millisecond
	g.u.First = time.Hour
	g.u.chk.Lock()
	g.u.RunningSeq, g.u.loaded = seq, false
	g.u.chk.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.u.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (b *qpBench) active() string { return strings.TrimSpace(readFile(b.qp + "/state/active")) }

func TestRealQPUpdateVideoMissingDuringTrial(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	g.u.RunningSeq = 5
	g.rel.publish(6, armELF(1_200_000, 1), nil)
	if got := g.check(t); got != ResInstalling || !b.paths.Trial() {
		t.Fatalf("installation : %q (%s)", got, g.u.Status().Message)
	}
	b.videoFailing(t, 6) // la 6 démarre à l'essai et ne rend pas le flux
	waitFor(t, "retour arrière", func() bool { return !b.paths.Trial() })
	if a := b.active(); a != "good" {
		t.Errorf("emplacement actif %q, good attendu", a)
	}
	waitFor(t, "état noté", func() bool { return g.u.Status().Result == ResRolledBack })
	if s := g.u.Status(); s.FailedSeq != 6 || s.PendingSeq != 0 || !strings.Contains(s.Message, "flux vidéo") {
		t.Errorf("état : %+v", s)
	}
}

func TestRealQPUpdateVideoMissingAfterValidation(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	g.u.RunningSeq = 5
	g.rel.publish(6, armELF(1_200_000, 1), nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("installation : %q", got)
	}
	// QIARA Plus valide la 6 (cache fermé pendant l'essai : rien à constater) ; prev garde la 5
	if out, err := exec.Command("sh", b.qp+"/qp-update", "promote", "--force").CombinedOutput(); err != nil {
		t.Fatalf("promote : %v %s", err, out)
	}
	if b.paths.Trial() {
		t.Fatal("essai toujours posé après la validation")
	}
	b.videoFailing(t, 6) // le cache s'ouvre plus tard : pas de flux
	waitFor(t, "retour à la version précédente", func() bool { return b.active() == "prev" })
	waitFor(t, "état noté", func() bool { return g.u.Status().Result == ResRolledBack })
	if s := g.u.Status(); s.FailedSeq != 6 || !strings.Contains(s.Message, "use-prev") && !strings.Contains(s.Message, "version précédente") {
		t.Errorf("état : %+v", s)
	}
}
