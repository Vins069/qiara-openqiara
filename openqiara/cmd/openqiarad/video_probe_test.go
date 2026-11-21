package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/caligone/openqiara/internal/camera"
	"github.com/caligone/openqiara/internal/mediahub"
	"github.com/caligone/openqiara/internal/nal"
)

// En-têtes H.265 (2 octets) : IDR_W_RADL (19, image clé) et TRAIL_R (1, tranche ordinaire).
var (
	h265Key   = []byte{19 << 1, 0x01, 0xaf, 0x00}
	h265Slice = []byte{1 << 1, 0x01, 0xaf, 0x00}
)

// scripted : une source qui envoie les échantillons donnés, puis se tait sans fermer.
func scripted(samples ...camera.Sample) mediahub.Source {
	return func(ctx context.Context) (<-chan camera.Sample, error) {
		ch := make(chan camera.Sample)
		go func() {
			for _, s := range samples {
				select {
				case ch <- s:
				case <-ctx.Done():
					return
				}
			}
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	}
}

func video(n int, keyAt int) []camera.Sample {
	out := make([]camera.Sample, 0, n)
	for i := 0; i < n; i++ {
		d := h265Slice
		if i == keyAt {
			d = h265Key
		}
		out = append(out, camera.Sample{IsVideo: true, Data: d})
	}
	return out
}

func probe(t *testing.T, src mediahub.Source, wait time.Duration) error {
	t.Helper()
	hub := mediahub.New("essai", src, nil, nil)
	hub.SetCodecState(nal.NewState(nal.H265, nil))
	return probeVideo(context.Background(), hub, nal.H265, wait)
}

func TestProbeVideoFlowing(t *testing.T) {
	if err := probe(t, scripted(video(40, 3)...), 2*time.Second); err != nil {
		t.Fatalf("flux normal refusé : %v", err)
	}
}

func TestProbeVideoNothing(t *testing.T) {
	err := probe(t, scripted(), 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "aucune image") {
		t.Fatalf("aucun flux : %v", err)
	}
}

func TestProbeVideoNoKeyFrame(t *testing.T) {
	err := probe(t, scripted(video(40, -1)...), 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "image clé : non") {
		t.Fatalf("flux sans image clé accepté : %v", err)
	}
}

func TestProbeVideoAudioOnly(t *testing.T) {
	audio := make([]camera.Sample, 50)
	for i := range audio {
		audio[i] = camera.Sample{Data: []byte{1, 2, 3, 4}}
	}
	if err := probe(t, scripted(audio...), 200*time.Millisecond); err == nil {
		t.Fatal("du son seul passe pour un flux vidéo")
	}
}

func TestProbeVideoTooFewUnits(t *testing.T) {
	err := probe(t, scripted(video(probeMinUnits-1, 0)...), 300*time.Millisecond)
	if err == nil {
		t.Fatalf("%d unités acceptées (il en faut %d)", probeMinUnits-1, probeMinUnits)
	}
}

func TestProbeVideoCancelled(t *testing.T) {
	hub := mediahub.New("essai", scripted(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probeVideo(ctx, hub, nal.H265, time.Minute); err == nil {
		t.Fatal("arrêt du daemon ignoré")
	}
}
