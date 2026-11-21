package mqtt

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// haSlug imite le nom d'entité que Home Assistant fabrique (minuscules, accents retirés, autres
// caractères remplacés par « _ ») pour les libellés français de la caméra.
func haSlug(s string) string {
	r := strings.NewReplacer("à", "a", "â", "a", "é", "e", "è", "e", "ê", "e", "ë", "e", "î", "i", "ï", "i",
		"ô", "o", "ù", "u", "û", "u", "ç", "c", "œ", "oe", "₂", "2")
	s = r.Replace(strings.ToLower(s))
	return strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "_"), "_")
}

// Revue V17 : les entités nommées dans le paquet d'alertes de la mise à jour signée existent bien dans
// la découverte MQTT de la caméra (appareil « OpenQiara Caméra » + nom de l'entité).
func TestSignedUpdateHAPackageEntities(t *testing.T) {
	b, err := os.ReadFile("../../../home-assistant/packages/openqiara_claude_v17_mise_a_jour_signee.yaml")
	if err != nil {
		t.Skip("paquet Home Assistant absent")
	}
	known := map[string]bool{}
	dev := haSlug(cameraDevice().Name)
	for _, f := range diagFields {
		known[f.Domain+"."+dev+"_"+haSlug(f.Name)] = true
	}
	used := regexp.MustCompile(`(sensor|binary_sensor)\.openqiara_[a-z0-9_]+`).FindAllString(string(b), -1)
	if len(used) == 0 {
		t.Fatal("aucune entité trouvée")
	}
	for _, e := range used {
		if !known[e] {
			t.Errorf("entité %s absente de la découverte MQTT", e)
		}
	}
}
