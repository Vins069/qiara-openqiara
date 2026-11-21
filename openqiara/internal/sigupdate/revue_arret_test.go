package sigupdate

import (
	"context"
	"testing"
	"time"
)

// Revue V17 : l'arrêt du daemon pendant « qp-update stage » ne doit pas tuer l'outil au milieu de
// l'écriture de l'état de QIARA Plus (sur claude17 le contexte de dépôt dérivait de celui du daemon :
// annulé, exec.CommandContext tuait sh qp-update).
func TestStageNotInterruptedByShutdown(t *testing.T) {
	g := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	var stageErr, rollbackErr error
	g.u.Stage = func(sctx context.Context, _, _ string) error {
		cancel() // arrêt du daemon pendant le dépôt
		time.Sleep(20 * time.Millisecond)
		stageErr = sctx.Err()
		if _, ok := sctx.Deadline(); !ok {
			t.Error("dépôt sans délai")
		}
		return nil
	}
	g.u.Disarmed = func() bool { return ctx.Err() == nil } // armée après coup : annulation de l'essai
	g.u.Rollback = func(rctx context.Context) error { g.rollbacks++; rollbackErr = rctx.Err(); return nil }
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	g.u.CheckOnce(ctx)
	if stageErr != nil {
		t.Errorf("qp-update stage interrompu par l'arrêt du daemon : %v", stageErr)
	}
	if g.rollbacks != 1 || rollbackErr != nil {
		t.Errorf("annulation : %d, contexte %v", g.rollbacks, rollbackErr)
	}
}

// Un dépôt en échec qui a pourtant posé l'essai (qp-update arrêté par son délai après l'écriture du
// marqueur) est annulé ; sur claude17 l'essai restait posé, sans relance ni annulation.
func TestStageFailureAfterTrialMarkerIsUndone(t *testing.T) {
	g := newRig(t)
	g.u.Stage = func(context.Context, string, string) error { g.trial.Store(true); return errTest }
	g.u.Rollback = func(context.Context) error { g.rollbacks++; g.trial.Store(false); return nil }
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResError || g.rollbacks != 1 || g.trial.Load() || g.restarts != 0 {
		t.Fatalf("résultat %q, annulations %d, essai %v, relances %d", got, g.rollbacks, g.trial.Load(), g.restarts)
	}
	// un dépôt refusé sans essai posé n'annule rien
	g.u.Stage = func(context.Context, string, string) error { return errTest }
	if got := g.check(t); got != ResError || g.rollbacks != 1 {
		t.Fatalf("résultat %q, annulations %d", got, g.rollbacks)
	}
}
