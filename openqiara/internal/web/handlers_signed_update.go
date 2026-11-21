package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/caligone/openqiara/internal/config"
	"github.com/caligone/openqiara/internal/sigupdate"
)

var signedChannelRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// signedUpdateView : réglages + disponibilité + état, tel que le renvoie l'API.
type signedUpdateView struct {
	Enabled       bool   `json:"enabled"`
	Repo          string `json:"repo"`
	Channel       string `json:"channel"`
	IntervalHours int    `json:"interval_hours"`
	// Available : au moins une clé de publication gravée ET QIARA Plus installé.
	// Sans cela, rien n'est jamais installé (enabled peut valoir vrai : c'est le réglage par défaut).
	Available bool             `json:"available"`
	Status    sigupdate.Status `json:"status"`
}

func (s *Server) signedView(su *sigupdate.Updater) signedUpdateView {
	c := s.store.Get().SignedUpdate
	v := signedUpdateView{Enabled: c.IsEnabled(), Repo: c.EffectiveRepo(), Channel: c.Channel, IntervalHours: c.IntervalHours}
	if v.Channel == "" {
		v.Channel = "stable"
	}
	if v.IntervalHours == 0 {
		v.IntervalHours = int(sigupdate.DefaultInterval / time.Hour)
	}
	v.Status = su.Status()
	v.Available = v.Status.Keys > 0 && qplusInstalled()
	return v
}

// handleGetSignedUpdate : réglages et état de la mise à jour signée. 503 si non branchée.
func (s *Server) handleGetSignedUpdate(w http.ResponseWriter, _ *http.Request) {
	su := s.signed.Load()
	if su == nil {
		writeErr(w, http.StatusServiceUnavailable, "mise à jour signée non configurée")
		return
	}
	writeJSON(w, http.StatusOK, s.signedView(su))
}

// handlePutSignedUpdate modifie les réglages (champs absents : inchangés). Pris en compte à la
// prochaine vérification, sans redémarrage. Activer exige une clé gravée et QIARA Plus ; repo vide
// revient au dépôt de la distribution (config.DefaultSignedUpdateRepo).
// Cette route ne déclenche aucune installation : seule la vérification périodique, alarme
// désarmée, installe. L'installation automatique de l'amont (PUT /config/update) reste refusée.
func (s *Server) handlePutSignedUpdate(w http.ResponseWriter, r *http.Request) {
	su := s.signed.Load()
	if su == nil {
		writeErr(w, http.StatusServiceUnavailable, "mise à jour signée non configurée")
		return
	}
	var body struct {
		Enabled       *bool   `json:"enabled"`
		Repo          *string `json:"repo"`
		Channel       *string `json:"channel"`
		IntervalHours *int    `json:"interval_hours"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON invalide")
		return
	}
	if body.Enabled == nil && body.Repo == nil && body.Channel == nil && body.IntervalHours == nil {
		writeErr(w, http.StatusBadRequest, "aucun champ à modifier (enabled, repo, channel, interval_hours)")
		return
	}
	next := s.store.Get().SignedUpdate
	if body.Enabled != nil {
		next.Enabled = body.Enabled
	}
	if body.Repo != nil {
		if *body.Repo != "" && !sigupdate.RepoRe.MatchString(*body.Repo) {
			writeErr(w, http.StatusBadRequest, "repo : forme « propriétaire/dépôt » attendue")
			return
		}
		next.Repo = *body.Repo
	}
	if body.Channel != nil {
		if *body.Channel != "" && !signedChannelRe.MatchString(*body.Channel) {
			writeErr(w, http.StatusBadRequest, "channel : minuscules, chiffres et tirets (16 caractères au plus)")
			return
		}
		next.Channel = *body.Channel
	}
	if body.IntervalHours != nil {
		h := *body.IntervalHours
		if h != 0 && (h < int(sigupdate.MinInterval/time.Hour) || h > int(sigupdate.MaxInterval/time.Hour)) {
			writeErr(w, http.StatusBadRequest, "interval_hours : 0 (12 h) ou de 6 à 168")
			return
		}
		next.IntervalHours = h
	}
	// Seule une demande d'activation est contrôlée : activée par défaut, la fonction se règle aussi
	// sur un binaire sans clé (elle y reste inerte).
	if body.Enabled != nil && *body.Enabled {
		st := su.Status()
		switch {
		case st.Keys == 0:
			writeErr(w, http.StatusConflict, "Mise à jour signée indisponible : aucune clé de publication n'est gravée dans ce binaire.")
			return
		case !qplusInstalled():
			writeErr(w, http.StatusConflict, "Mise à jour signée indisponible : QIARA Plus n'est pas installé (c'est lui qui installe, essaie et annule).")
			return
		}
	}
	if err := s.store.Update(func(cfg *config.Config) { cfg.SignedUpdate = next }); err != nil {
		writeErr(w, http.StatusInternalServerError, "échec sauvegarde: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.signedView(su))
}
