package sigupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State est la mémoire persistante de la mise à jour signée (/data/qp/sigupdate.json) :
// une écriture par vérification (toutes les 6 h au plus) et quelques-unes par version installée.
type State struct {
	LastCheck   time.Time `json:"last_check,omitempty"`
	Result      string    `json:"result,omitempty"`  // code court, voir les constantes Res*
	Message     string    `json:"message,omitempty"` // phrase pour l'humain
	SeenSeq     int64     `json:"seen_seq,omitempty"`
	SeenVersion string    `json:"seen_version,omitempty"`
	HighSeq     int64     `json:"high_seq,omitempty"` // plus grand seq jamais vu signé : refus d'un seq inférieur (retour arrière imposé par le serveur)
	PendingSeq  int64     `json:"pending_seq,omitempty"`
	PendingSHA  string    `json:"pending_sha,omitempty"`
	PendingAt   time.Time `json:"pending_at,omitempty"`
	FailedSeq   int64     `json:"failed_seq,omitempty"` // plus grand seq revenu en arrière : jamais réessayé
	// InstalledSeq : plus grand seq constaté en service (hors essai). Une version qui tourne avec un seq
	// inférieur signifie qu'on l'a quittée (bouton « version précédente » de QIARA Plus, réinstallation à
	// la main, repli de QIARA Plus) : elle n'est alors pas réinstallée automatiquement.
	InstalledSeq int64 `json:"installed_seq,omitempty"`
}

// Codes de résultat.
const (
	ResDisabled   = "desactivee"
	ResNoKey      = "pas-de-cle"
	ResUpToDate   = "a-jour"
	ResWaiting    = "attente"      // alarme armée ou état inconnu, essai en cours, QIARA Plus absent
	ResNetwork    = "echec-reseau" // dépôt injoignable, réponse invalide ou trop grosse
	ResSignature  = "echec-signature"
	ResRefused    = "refusee"      // seq/canal/min_seq/échec antérieur
	ResInstalling = "installation" // déposée chez QIARA Plus, relance demandée ou essai en cours
	ResInstalled  = "installee"
	ResRolledBack = "retour-arriere" // QIARA Plus est revenu à la version précédente
	ResError      = "erreur"
)

func loadState(path string) State {
	var s State
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if json.Unmarshal(b, &s) != nil {
		return State{} // fichier abîmé : on repart de zéro (le seq installé vient du binaire, pas d'ici)
	}
	return s
}

// saveState écrit atomiquement : fichier temporaire synchronisé sur la carte, puis renommage, puis
// synchronisation du dossier (comme config.Store et l'état de l'alarme de l'amont rc.25). Une coupure
// de courant laisse l'ancien fichier ou le nouveau, jamais un fichier vide : perdre cet état ferait
// oublier le plus grand seq vu (anti-retour-arrière) et les versions revenues en arrière.
func saveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", " ")
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
