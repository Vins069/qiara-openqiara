package sigupdate

import (
	"strings"
	"testing"
)

// restartAs simule un redémarrage du daemon sur un binaire de seq donné : l'état est relu du disque.
func (g *rig) restartAs(seq int64) {
	g.u.chk.Lock()
	g.u.RunningSeq, g.u.loaded = seq, false
	g.u.load()
	g.u.reconcile()
	g.u.chk.Unlock()
}

// Revue V17 : après « Revenir à la version précédente » (use-prev de QIARA Plus) ou une réinstallation à
// la main d'une version plus ancienne, la vérification suivante ne doit pas réinstaller la version
// qu'on vient d'écarter (sur claude17 : elle était retéléchargée et redéposée chez QIARA Plus).
func TestManualRollbackNotUndone(t *testing.T) {
	g := newRig(t)
	g.u.RunningSeq = 5
	g.rel.publish(6, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("installation de 6 : %q", got)
	}
	// la 6 démarre, passe l'essai et est validée
	g.restartAs(6)
	if got := g.check(t); got != ResUpToDate {
		t.Fatalf("6 en service : %q (%s)", got, g.u.Status().Message)
	}
	downloads, stages := g.rel.count("/openqiarad-test"), len(g.stage)
	// le propriétaire appuie sur « Revenir à la version précédente » : la 5 tourne de nouveau
	g.restartAs(5)
	if s := g.u.Status(); s.Result != ResRolledBack || s.FailedSeq != 6 {
		t.Errorf("retour manuel non reconnu : %+v", s)
	}
	if got := g.check(t); got != ResRefused {
		t.Fatalf("la version quittée est reproposée : %q (%s)", got, g.u.Status().Message)
	}
	if g.rel.count("/openqiarad-test") != downloads || len(g.stage) != stages || g.restarts != 1 {
		t.Errorf("la version écartée a été retéléchargée ou redéposée (téléchargements %d, dépôts %d, relances %d)",
			g.rel.count("/openqiarad-test"), len(g.stage), g.restarts)
	}
	// une version plus récente reste installée normalement
	g.rel.publish(7, fakeBinary(mb1, 2), nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("version suivante : %q", got)
	}
}

// Une première installation à la main (rien de mémorisé) ne passe pas pour un retour arrière, et un
// essai en cours (installation à la main par QIARA Plus) ne fait rien conclure.
func TestInstalledSeqFirstStartAndTrial(t *testing.T) {
	g := newRig(t)
	g.trial.Store(true)
	g.restartAs(17)
	if s := g.u.Status(); s.Result != "" || s.FailedSeq != 0 {
		t.Errorf("pendant un essai à la main : %+v", s)
	}
	g.trial.Store(false)
	g.restartAs(17)
	if s := g.u.Status(); s.Result != "" || s.FailedSeq != 0 {
		t.Errorf("premier démarrage de la 17 : %+v", s)
	}
	if st := loadState(g.u.StatePath); st.InstalledSeq != 17 {
		t.Errorf("seq en service non mémorisé : %+v", st)
	}
	g.restartAs(16)
	if s := g.u.Status(); s.Result != ResRolledBack || s.FailedSeq != 17 || !strings.Contains(s.Message, "17") {
		t.Errorf("retour de 17 à 16 : %+v", s)
	}
}
