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
