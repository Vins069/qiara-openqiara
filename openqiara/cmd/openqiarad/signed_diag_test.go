package main

import (
	"testing"

	"github.com/caligone/openqiara/internal/sigupdate"
)

func TestSignedDiagNothingWithoutKey(t *testing.T) {
	if got := signedDiag(sigupdate.Status{Keys: 0, RunningSeq: 3, Result: sigupdate.ResNoKey}); got != nil {
		t.Errorf("relevé sans clé gravée : %v", got)
	}
}

func TestSignedDiagWithKey(t *testing.T) {
	got := signedDiag(sigupdate.Status{Keys: 1, RunningSeq: 17, Active: true, Result: sigupdate.ResUpToDate,
		SeenVersion: "v0.1.0-rc.25-claude-v17", LastCheck: "2026-11-20T12:00:00Z"})
	if got["su_running_seq"] != int64(17) || got["su_result"] != "À jour" ||
		got["su_seen_version"] != "v0.1.0-rc.25-claude-v17" || got["su_last_check"] != "2026-11-20T12:00:00Z" {
		t.Errorf("relevé : %v", got)
	}
	// avant la première vérification : pas de version vue ni d'heure, un état lisible quand même
	got = signedDiag(sigupdate.Status{Keys: 1, Active: true})
	if got["su_result"] != "Pas encore vérifiée" || got["su_seen_version"] != nil || got["su_last_check"] != nil {
		t.Errorf("avant vérification : %v", got)
	}
	got = signedDiag(sigupdate.Status{Keys: 1})
	if got["su_result"] != "Désactivée" {
		t.Errorf("désactivée : %v", got)
	}
}

func TestSignedDiagLabelsCoverEveryResult(t *testing.T) {
	for _, r := range []string{sigupdate.ResDisabled, sigupdate.ResNoKey, sigupdate.ResUpToDate, sigupdate.ResWaiting, sigupdate.ResNetwork,
		sigupdate.ResSignature, sigupdate.ResRefused, sigupdate.ResInstalling, sigupdate.ResInstalled, sigupdate.ResRolledBack, sigupdate.ResError} {
		if signedUpdateLabels[r] == "" {
			t.Errorf("résultat %q sans libellé", r)
		}
	}
}

func TestRunningSignedSeq(t *testing.T) {
	old := signedSeq
	defer func() { signedSeq = old }()
	for in, want := range map[string]int64{"0": 0, "17": 17, "": 0, "abc": 0, "-3": 0, "99999999999": 99999999999} {
		signedSeq = in
		if got := runningSignedSeq(); got != want {
			t.Errorf("%q -> %d, %d attendu", in, got, want)
		}
	}
}
