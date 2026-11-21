package sigupdate

import (
	"context"
	"fmt"
	"time"
)

// Contrôle du flux vidéo (décision du propriétaire, 21/11/2026).
//
// Avant la relance : cache ouvert, la vidéo doit couler avec la version en service, sinon l'installation
// attend. On ne relance pas pendant un incident vidéo (QIARA Plus ou la reprise de hlcamd peuvent être en
// train de le traiter), et la nouvelle version ne doit pas être jugée sur un flux déjà absent. Cache fermé,
// le flux est en pause par conception (#50) : il n'y a rien à couper ni à contrôler.
//
// Après la relance, la nouvelle version contrôle elle-même son flux dès que le cache est ouvert (au
// démarrage, ou plus tard s'il était fermé) :
//   - pendant l'essai de QIARA Plus, sa santé reste « non valide » jusqu'au flux constaté : QIARA Plus ne
//     la valide pas avant ;
//   - sans flux pendant VideoDeadline (cache ouvert), elle demande le retour : essai en cours, qp-update
//     rollback ; déjà validée (cache resté fermé pendant l'essai), qp-update use-prev. Jamais alarme armée
//     ni état inconnu : le retour attend le désarmement. La version est alors notée en échec et n'est plus
//     proposée.
//
// Ce contrôle est fait par la version installée, pas par celle qui l'installe : il protège les mises à jour
// vers des versions qui le contiennent.

// Délais par défaut du contrôle du flux.
const (
	defaultVideoPreWait  = 20 * time.Second // avant la relance : le flux doit couler dans ce délai
	defaultVideoWindow   = 30 * time.Second // après la relance : une écoute du flux
	defaultVideoDeadline = 5 * time.Minute  // après la relance : écoutes sans flux, cache ouvert, avant le retour
	defaultVideoPoll     = 10 * time.Second // ouverture du cache, désarmement, écoute suivante
)

func (u *Updater) shutterOpen() bool { return u.ShutterOpen == nil || u.ShutterOpen() }

// videoBefore : nil si la relance peut avoir lieu du point de vue du flux vidéo. Sous chk.
func (u *Updater) videoBefore(ctx context.Context) error {
	if u.VideoProbe == nil || !u.shutterOpen() {
		return nil
	}
	return u.VideoProbe(ctx, orDur(u.VideoPreWait, defaultVideoPreWait))
}

// HealthHold : non vide tant que la santé écrite pour QIARA Plus doit rester « non valide », c'est-à-dire
// pendant l'essai d'une version signée dont le flux vidéo n'est pas encore constaté (cache ouvert). La
// phrase dit pourquoi (champ app_err).
func (u *Updater) HealthHold() string {
	if p := u.hold.Load(); p != nil {
		return *p
	}
	return ""
}

func (u *Updater) setHold(msg string) {
	if msg == "" {
		u.hold.Store(nil)
		return
	}
	u.hold.Store(&msg)
}

// videoPending : la version signée seq qui tourne attend-elle encore son contrôle du flux ? Lit
// l'instantané publié (jamais chk : une vérification peut le tenir plusieurs minutes).
func (u *Updater) videoPending(seq int64) bool {
	u.smu.Lock()
	defer u.smu.Unlock()
	return u.pubLoaded && u.pub.VideoSeq == seq
}

func (u *Updater) trial() bool { return u.Trial != nil && u.Trial() }

func (u *Updater) disarmed() bool { return u.Disarmed != nil && u.Disarmed() }

// sleep attend d, faux si ctx se termine avant.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// watchVideo contrôle le flux de la version signée seq qui vient d'être installée et tourne.
func (u *Updater) watchVideo(ctx context.Context, seq int64) {
	defer u.setHold("")
	poll := orDur(u.VideoPoll, defaultVideoPoll)
	window := orDur(u.VideoWindow, defaultVideoWindow)
	deadline := orDur(u.VideoDeadline, defaultVideoDeadline)
	var since time.Time // début de l'absence de flux, cache ouvert sans interruption
	for ctx.Err() == nil && u.videoPending(seq) {
		if !u.shutterOpen() {
			u.setHold("") // cache fermé : rien à constater, QIARA Plus juge comme d'habitude
			since = time.Time{}
			if !sleep(ctx, poll) {
				return
			}
			continue
		}
		if u.trial() {
			u.setHold(fmt.Sprintf("flux vidéo de la version signée %d pas encore constaté", seq))
		} else {
			u.setHold("")
		}
		start := time.Now()
		err := u.VideoProbe(ctx, window)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			u.videoConfirmed(seq)
			return
		}
		switch {
		case !u.shutterOpen(): // fermé pendant l'écoute : elle ne compte pas
			since = time.Time{}
			continue
		case since.IsZero():
			since = start
		}
		if time.Since(since) >= deadline {
			u.videoFailed(ctx, seq, err)
			return
		}
		if !sleep(ctx, poll) {
			return
		}
	}
}

func (u *Updater) videoConfirmed(seq int64) {
	u.chk.Lock()
	defer u.chk.Unlock()
	u.load()
	if u.st.VideoSeq != seq {
		return
	}
	u.st.VideoSeq = 0
	if err := saveState(u.StatePath, u.st); err != nil {
		u.logger().Warn("mise à jour signée : état non enregistré", "error", err)
	}
	u.publish()
	u.logger().Info("mise à jour signée : flux vidéo constaté avec la nouvelle version", "seq", seq)
}

// videoFailed : pas de flux avec la version signée seq. Retour à la version précédente, alarme désarmée.
func (u *Updater) videoFailed(ctx context.Context, seq int64, why error) {
	msg := fmt.Sprintf("la version signée %d n'a pas rendu le flux vidéo (%v)", seq, why)
	waiting := false
	for !u.disarmed() {
		if !waiting {
			u.chk.Lock()
			u.load()
			if u.st.VideoSeq == seq {
				u.note(ResWaiting, msg+" : retour à la version précédente dès que l'alarme sera désarmée", false)
			}
			u.chk.Unlock()
			waiting = true
		}
		if !sleep(ctx, orDur(u.VideoPoll, defaultVideoPoll)) {
			return
		}
	}
	u.chk.Lock()
	u.load()
	if u.st.VideoSeq != seq || u.RunningSeq != seq {
		u.chk.Unlock()
		return
	}
	trial := u.trial()
	if seq > u.st.FailedSeq {
		u.st.FailedSeq = seq
	}
	u.st.VideoSeq = 0
	if u.st.PendingSeq == seq {
		u.st.PendingSeq, u.st.PendingSHA, u.st.PendingAt = 0, "", time.Time{}
	}
	how, act := "QIARA Plus annule l'essai", u.Rollback
	if !trial {
		how, act = "retour à la version précédente de QIARA Plus", u.UsePrev
	}
	// Noté avant le geste : qp-update arrête ce processus (le lanceur relance la version précédente).
	u.note(ResRolledBack, msg+" : "+how+", elle ne sera pas réessayée", false)
	u.chk.Unlock()
	if act == nil {
		u.logger().Error("mise à jour signée : retour arrière impossible (outil absent)")
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute) // jamais interrompu
	defer cancel()
	if err := act(actx); err != nil {
		u.chk.Lock()
		u.note(ResError, msg+" : retour à la version précédente impossible ("+err.Error()+")", false)
		u.chk.Unlock()
	}
}
