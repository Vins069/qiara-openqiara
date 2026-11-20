package mqtt

import "testing"

// Les clés du relevé de la mise à jour signée (cmd/openqiarad/diag.go) ont chacune son entité.
func TestDiagFieldsHaveSignedUpdateKeys(t *testing.T) {
	have := map[string]bool{}
	for _, f := range diagFields {
		have[f.Key] = true
	}
	for _, k := range []string{"su_result", "su_running_seq", "su_seen_version", "su_last_check"} {
		if !have[k] {
			t.Errorf("entité %s absente de diagFields", k)
		}
		if guardFed(k) {
			t.Errorf("%s ne vient pas du gardien de QIARA Plus", k)
		}
	}
}
