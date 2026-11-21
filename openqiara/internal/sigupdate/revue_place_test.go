package sigupdate

import (
	"os"
	"testing"
)

// Revue V17 : à la première vérification, le dossier de dépôt n'existe pas encore ; sur claude17 la
// lecture de la place échouait et le contrôle était sauté (téléchargement sur une carte pleine).
func TestFreeSpaceCheckedOnFirstUse(t *testing.T) {
	g := newRig(t)
	g.u.FreeSpace = func(dir string) (uint64, error) {
		if _, err := os.Stat(dir); err != nil {
			return 0, err // comme statfs sur un dossier absent
		}
		return 2 << 20, nil
	}
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != 0 || len(g.stage) != 0 {
		t.Fatalf("résultat %q, téléchargements %d (%s)", got, g.rel.count("/openqiarad-test"), g.u.Status().Message)
	}
}

// Il faut la place de deux binaires (fichier téléchargé + copie de qp-update dans new) et la réserve.
func TestFreeSpaceForTwoCopies(t *testing.T) {
	g := newRig(t)
	g.u.FreeSpace = func(string) (uint64, error) { return uint64(mb1) + reserveBytes + 1, nil }
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != 0 {
		t.Fatalf("place pour une seule copie acceptée : %q", got)
	}
	g.u.FreeSpace = func(string) (uint64, error) { return 2*uint64(mb1) + reserveBytes, nil }
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("place suffisante refusée : %q (%s)", got, g.u.Status().Message)
	}
}

// Place illisible : rien n'est téléchargé.
func TestFreeSpaceUnreadable(t *testing.T) {
	g := newRig(t)
	g.u.FreeSpace = func(string) (uint64, error) { return 0, errTest }
	g.rel.publish(5, fakeBinary(mb1, 1), nil)
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != 0 {
		t.Fatalf("résultat %q", got)
	}
}
