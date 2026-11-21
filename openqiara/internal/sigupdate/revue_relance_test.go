package sigupdate

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// orderRig : un ordre de relance simulé. waiting : l'ordre attend le surveillant ; taken : le
// surveillant le prend au moment du retrait (WithdrawOrder échoue).
type orderRig struct {
	waiting   atomic.Bool
	takenLate bool
	withdrawn atomic.Int32
}

func (g *rig) watchOrder(o *orderRig) {
	g.u.OrderWaiting = func() bool { return o.waiting.Load() }
	g.u.WithdrawOrder = func() bool {
		if o.takenLate || !o.waiting.Load() {
			o.waiting.Store(false)
			return false
		}
		o.waiting.Store(false)
		o.withdrawn.Add(1)
		return true
	}
	g.u.OrderTimeout, g.u.RestartGrace, g.u.PollEvery = 300*time.Millisecond, 300*time.Millisecond, 5*time.Millisecond
	g.u.Restart = func() error { g.restarts++; o.waiting.Store(true); return g.restartErr }
}

// Revue V17 : l'alarme s'arme après l'ordre de relance mais avant que QIARA Plus le prenne. Sur
// claude17, l'essai restait déposé (emplacement new actif) et l'ordre exécuté quelques secondes plus
// tard, alarme armée ; désormais l'ordre est retiré et l'essai annulé.
func TestRestartOrderWithdrawnWhenAlarmArms(t *testing.T) {
	g := newRig(t)
	o := &orderRig{}
	g.watchOrder(o)
	calls := 0
	g.u.Disarmed = func() bool { calls++; return calls <= 3 } // les 3 contrôles d'avant l'ordre, puis armée
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || !strings.Contains(g.u.Status().Message, "alarme armée avant la relance") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if o.withdrawn.Load() != 1 || g.rollbacks != 1 || g.u.Status().PendingSeq != 0 || g.u.Status().FailedSeq != 0 {
		t.Errorf("retraits %d, annulations %d, état %+v", o.withdrawn.Load(), g.rollbacks, g.u.Status())
	}
}

// QIARA Plus ne prend pas l'ordre (surveillant bloqué) : retiré au bout du délai, essai annulé.
func TestRestartOrderNotTaken(t *testing.T) {
	g := newRig(t)
	o := &orderRig{}
	g.watchOrder(o)
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || !strings.Contains(g.u.Status().Message, "n'a pas pris l'ordre") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if o.withdrawn.Load() != 1 || g.rollbacks != 1 || g.u.Status().PendingSeq != 0 {
		t.Errorf("retraits %d, annulations %d, état %+v", o.withdrawn.Load(), g.rollbacks, g.u.Status())
	}
}

// L'ordre est pris mais rien ne relance l'application (limite de 6 relances par heure du surveillant,
// mode passif, surveillant sans cet ordre) : l'essai déposé est annulé au lieu de partir au prochain
// démarrage venu.
func TestRestartTakenButNotDone(t *testing.T) {
	g := newRig(t)
	o := &orderRig{}
	g.watchOrder(o)
	g.u.Restart = func() error { g.restarts++; return nil } // pris aussitôt (jamais en attente), sans effet
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || !strings.Contains(g.u.Status().Message, "n'a pas relancé") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if g.rollbacks != 1 || g.u.Status().PendingSeq != 0 {
		t.Errorf("annulations %d, état %+v", g.rollbacks, g.u.Status())
	}
	// la vérification suivante recommence normalement
	g.u.Restart = func() error { g.restarts++; return nil }
	g.u.OrderWaiting, g.u.WithdrawOrder = nil, nil
	if got := g.check(t); got != ResInstalling {
		t.Errorf("nouvel essai : %q", got)
	}
}

// L'ordre est pris par le surveillant au moment même où l'alarme s'arme (retrait impossible) : la
// relance a lieu, rien n'est annulé ; l'arrêt du daemon (contexte annulé) termine la vérification.
func TestRestartTakenJustInTime(t *testing.T) {
	g := newRig(t)
	o := &orderRig{takenLate: true}
	g.watchOrder(o)
	g.u.RestartGrace = time.Minute
	calls := 0
	g.u.Disarmed = func() bool { calls++; return calls <= 3 }
	ctx, cancel := context.WithCancel(context.Background())
	g.u.Restart = func() error {
		g.restarts++
		o.waiting.Store(true)
		time.AfterFunc(100*time.Millisecond, cancel) // le surveillant arrête le daemon
		return nil
	}
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	done := make(chan string, 1)
	go func() { done <- g.u.CheckOnce(ctx) }()
	select {
	case got := <-done:
		if got != ResInstalling || g.rollbacks != 0 || g.u.Status().PendingSeq != 5 {
			t.Errorf("résultat %q, annulations %d, état %+v", got, g.rollbacks, g.u.Status())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("vérification bloquée")
	}
}

// Relance normale : l'ordre est pris puis le daemon est arrêté ; l'état dit « installation » dès
// l'ordre donné (visible même si le processus disparaît aussitôt).
func TestRestartNormalPath(t *testing.T) {
	g := newRig(t)
	o := &orderRig{}
	g.watchOrder(o)
	g.u.RestartGrace = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	g.u.Restart = func() error {
		g.restarts++
		o.waiting.Store(true)
		time.AfterFunc(20*time.Millisecond, func() { o.waiting.Store(false) }) // pris
		time.AfterFunc(80*time.Millisecond, cancel)                            // arrêté
		return nil
	}
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.u.CheckOnce(ctx); got != ResInstalling || g.rollbacks != 0 || o.withdrawn.Load() != 0 {
		t.Fatalf("résultat %q, annulations %d, retraits %d", got, g.rollbacks, o.withdrawn.Load())
	}
	if st := loadState(g.u.StatePath); st.Result != ResInstalling || st.PendingSeq != 5 {
		t.Errorf("état sur le disque : %+v", st)
	}
}
