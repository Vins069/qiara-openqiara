package web

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/caligone/openqiara/internal/sigupdate"
)

// Revue V17 : le schéma du PUT /api/v1/update/signed (docs/openapi.json) décrit ce que le serveur
// accepte. Sur claude17, son motif refusait repo "" (effacer le dépôt), que le serveur accepte.
func TestOpenAPISignedUpdatePatchMatchesServer(t *testing.T) {
	b, err := os.ReadFile("../../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Pattern string `json:"pattern"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	pat := doc.Components.Schemas["SignedUpdatePatch"].Properties["repo"].Pattern
	re, err := regexp.Compile(pat)
	if err != nil {
		t.Fatalf("motif %q : %v", pat, err)
	}
	for _, repo := range []string{"", "Vins069/qiara-openqiara", "a/b", "x/../y", "a/b/c", "-a/b", "a b/c"} {
		server := repo == "" || sigupdate.RepoRe.MatchString(repo)
		if re.MatchString(repo) != server {
			t.Errorf("repo %q : schéma %v, serveur %v", repo, re.MatchString(repo), server)
		}
	}
}
