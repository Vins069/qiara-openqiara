package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const signedHAPackage = "../../../home-assistant/packages/openqiara_claude_v17_mise_a_jour_signee.yaml"

// Revue V17 : le paquet Home Assistant d'alertes ne déclenche que sur des états réellement publiés par
// la caméra (libellés de signedUpdateLabels), et jamais au retour de l'entité (indisponible ou inconnue
// puis le dernier résultat, gardé par la caméra) : sur claude17, chaque redémarrage de la caméra ou de
// Home Assistant renvoyait la dernière alerte.
func TestSignedUpdateHAPackage(t *testing.T) {
	b, err := os.ReadFile(signedHAPackage)
	if err != nil {
		t.Skip("paquet Home Assistant absent")
	}
	labels := map[string]bool{}
	for _, l := range signedUpdateLabels {
		labels[l] = true
	}
	blocks := strings.Split(string(b), "- trigger: state")[1:]
	if len(blocks) != 4 {
		t.Fatalf("%d déclencheurs, 4 attendus", len(blocks))
	}
	quoted := regexp.MustCompile(`"([^"]+)"`)
	for i, blk := range blocks {
		blk = strings.SplitN(blk, "actions:", 2)[0]
		if !strings.Contains(blk, "entity_id: sensor.openqiara_camera_mise_a_jour_signee") {
			t.Errorf("déclencheur %d : autre entité", i+1)
		}
		if !regexp.MustCompile(`not_from: \["unavailable", "unknown"\]`).MatchString(blk) {
			t.Errorf("déclencheur %d : se déclenche au retour de l'entité (not_from manquant)", i+1)
		}
		to := strings.SplitN(blk, "to:", 2)
		if len(to) != 2 {
			t.Fatalf("déclencheur %d sans « to »", i+1)
		}
		states := quoted.FindAllStringSubmatch(strings.SplitN(to[1], "for:", 2)[0], -1)
		if len(states) == 0 {
			t.Errorf("déclencheur %d : aucun état", i+1)
		}
		for _, s := range states {
			if !labels[s[1]] {
				t.Errorf("déclencheur %d : état %q jamais publié par la caméra", i+1, s[1])
			}
		}
	}
}
