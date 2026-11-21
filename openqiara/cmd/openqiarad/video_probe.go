package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caligone/openqiara/internal/mediahub"
	"github.com/caligone/openqiara/internal/nal"
)

// probeMinUnits : unités vidéo (tranches d'image) à recevoir pour dire que le flux coule, soit environ
// deux secondes d'images. Il en faut aussi une d'image clé : un décodeur peut partir du flux.
const probeMinUnits = 25

// probeVideo dit si le flux vidéo coule, pour la mise à jour signée (contrôle avant la relance et
// après). Il s'abonne au concentrateur (ce qui démarre le pipeline s'il dormait, comme un client RTSP)
// et attend, au plus wait, probeMinUnits unités d'image du codec réglé dont une image clé. Le
// concentrateur jette déjà les unités d'un autre codec : un flux dans le mauvais codec échoue ici aussi.
func probeVideo(ctx context.Context, hub *mediahub.Hub, codec nal.Codec, wait time.Duration) error {
	if hub == nil {
		return errors.New("pas de pipeline vidéo")
	}
	sub := hub.Subscribe()
	defer sub.Close()
	t := time.NewTimer(wait)
	defer t.Stop()
	units, key := 0, false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if units == 0 {
				return fmt.Errorf("aucune image %s en %s", codec, wait)
			}
			return fmt.Errorf("%d unité(s) d'image %s en %s, image clé : %s", units, codec, wait, map[bool]string{true: "oui", false: "non"}[key])
		case s, ok := <-sub.Samples():
			if !ok {
				return errors.New("pipeline vidéo arrêté")
			}
			if !s.IsVideo || !nal.IsVCL(codec, s.Data) {
				continue
			}
			units++
			key = key || nal.IsKey(codec, s.Data)
			if units >= probeMinUnits && key {
				return nil
			}
		}
	}
}
