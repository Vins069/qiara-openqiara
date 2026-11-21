package sigupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// Revue V17 : un enregistrement qui échoue ne laisse pas de fichier temporaire sur /data (sur claude17,
// sigupdate.json.tmp restait à côté quand le renommage échouait), et l'état relu est le dernier écrit.
func TestSaveStateCleansUpAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sigupdate.json")
	if err := os.MkdirAll(filepath.Join(path, "occupé"), 0o755); err != nil { // un dossier à la place du fichier
		t.Fatal(err)
	}
	if err := saveState(path, State{HighSeq: 9}); err == nil {
		t.Fatal("renommage sur un dossier accepté")
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("fichier temporaire laissé après un échec")
	}
	os.RemoveAll(path)
	want := State{HighSeq: 9, FailedSeq: 7, InstalledSeq: 6, PendingSeq: 9, PendingSHA: "ab"}
	if err := saveState(path, want); err != nil {
		t.Fatal(err)
	}
	if got := loadState(path); got != want {
		t.Errorf("relu %+v, %+v attendu", got, want)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("fichier temporaire laissé après un succès")
	}
}
