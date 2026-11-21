package main

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/caligone/openqiara/internal/camera"
	"github.com/caligone/openqiara/internal/mediahub"
	"github.com/caligone/openqiara/internal/nal"
	"github.com/caligone/openqiara/internal/publisher"
	"github.com/caligone/openqiara/internal/qplus"
	"github.com/caligone/openqiara/internal/sigupdate"
	"github.com/caligone/openqiara/internal/sonde"
)

// diagPeriod is how often the camera's health goes to Home Assistant.
const diagPeriod = time.Minute

// serveDiagnostics publishes, every minute, what the camera knows of its own
// health: Wi-Fi link, memory, storage, temperature, air quality, and when
// each sensor was last heard. It rides on the MQTT connection and stops with
// ctx; a minute with no connection is skipped, the next one tells all. The
// video codec goes with it: the stream's, the configured one
// (the one served), and whether the two differ; kick asks for a reading
// now (the stream's codec just changed).
func serveDiagnostics(ctx context.Context, pub *publisher.MQTTPublisher, cam camera.Client, version string,
	codec *nal.State, hub *mediahub.Hub, signed *atomic.Pointer[sigupdate.Updater], kick <-chan struct{}, logger *slog.Logger) {
	probe := sonde.New()
	ha := pub.HAPublisher()
	tick := time.NewTicker(diagPeriod)
	defer tick.Stop()
	first := time.After(20 * time.Second) // let the broker connect first
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-tick.C:
		case <-kick:
		}
		if !ha.IsConnected() {
			continue
		}
		reading := map[string]any(probe.System())
		for k, v := range codecDiag(codec, hub) {
			reading[k] = v
		}
		for k, v := range probe.Vendor(ctx) {
			reading[k] = v
		}
		for k, v := range qplus.Default.MemoryDiag() {
			reading[k] = v
		}
		for k, v := range qplus.Default.Diag() {
			reading[k] = v
		}
		if u := signed.Load(); u != nil {
			for k, v := range signedDiag(u.Status()) {
				reading[k] = v
			}
		}
		reading["version"] = version
		reading["mqtt_reconnects"] = ha.Reconnects()
		seen := map[string]string{}
		for _, s := range cam.CachedSensors() {
			if s.LastSeen > 0 {
				seen[strconv.Itoa(s.ID)] = time.Unix(s.LastSeen, 0).UTC().Format(time.RFC3339)
			}
		}
		if len(seen) > 0 {
			reading["last_seen"] = seen
		}
		if err := ha.PublishDiag(ctx, reading); err != nil {
			logger.Debug("diagnostics not published", "error", err)
		}
	}
}

// codecDiag is the video codec's part of the diagnostic reading: the codec
// the stream showed on its last SPS (video_codec; the configured one
// before any), the one configured (video_codec_configured, what RTSP, HLS
// and HomeKit serve), and a problem flag when the stream contradicts the
// setting (it is then not served); the NAL counts since the start, units
// dropped for their codec included, as an attribute map (no entity of its
// own).
func codecDiag(codec *nal.State, hub *mediahub.Hub) map[string]any {
	if codec == nil {
		return nil
	}
	stream := codec.Detected()
	if stream == nal.Unknown {
		stream = codec.Configured()
	}
	r := map[string]any{
		"video_codec":            stream.String(),
		"video_codec_configured": codec.Configured().String(),
		"video_codec_mismatch":   "OFF",
	}
	if codec.Mismatch() {
		r["video_codec_mismatch"] = "ON"
	}
	if hub != nil {
		r["video_nal"] = hub.NALCounts()
	}
	return r
}

// signedUpdateLabels : le résultat de la dernière vérification, en clair pour Home Assistant.
var signedUpdateLabels = map[string]string{
	sigupdate.ResDisabled:   "Désactivée",
	sigupdate.ResNoKey:      "Inactive (aucune clé de publication)",
	sigupdate.ResUpToDate:   "À jour",
	sigupdate.ResWaiting:    "En attente (alarme, essai, QIARA Plus ou flux vidéo)",
	sigupdate.ResNetwork:    "Dépôt injoignable ou réponse refusée",
	sigupdate.ResSignature:  "Signature refusée",
	sigupdate.ResRefused:    "Version refusée",
	sigupdate.ResInstalling: "Installation en essai",
	sigupdate.ResInstalled:  "Installée",
	sigupdate.ResRolledBack: "Retour arrière",
	sigupdate.ResError:      "Erreur",
}

// signedDiag is the signed update's part of the diagnostic reading. Nothing at all while no
// publication key is engraved in the binary (the entities do not exist then): su_result is the
// last check's outcome, su_running_seq the signed version running (0: unsigned build),
// su_seen_version the last signed version the repository announced, su_last_check when.
func signedDiag(st sigupdate.Status) map[string]any {
	if st.Keys == 0 {
		return nil
	}
	out := map[string]any{"su_running_seq": st.RunningSeq}
	if st.Result != "" {
		if l, ok := signedUpdateLabels[st.Result]; ok {
			out["su_result"] = l
		} else {
			out["su_result"] = st.Result
		}
	} else if st.Active {
		out["su_result"] = "Pas encore vérifiée"
	} else {
		out["su_result"] = signedUpdateLabels[sigupdate.ResDisabled]
	}
	if st.SeenVersion != "" {
		out["su_seen_version"] = st.SeenVersion
	}
	if st.LastCheck != "" {
		out["su_last_check"] = st.LastCheck
	}
	return out
}
