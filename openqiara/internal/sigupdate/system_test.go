//go:build linux

package sigupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caligone/openqiara/internal/qplus"
)

func TestRunToolMessage(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "outil.sh")
	os.WriteFile(script, []byte("echo 'ERREUR: ELF non ARM' >&2\nexit 1\n"), 0o644)
	err := runTool(context.Background(), script, "stage", "x")
	if err == nil || !strings.Contains(err.Error(), "ELF non ARM") {
		t.Fatalf("err = %v", err)
	}
	if err := runTool(context.Background(), filepath.Join(dir, "absent.sh")); err == nil {
		t.Error("outil absent accepté")
	}
}

// armELF fabrique un faux binaire ELF 32 bits ARM (en-tête réel) de n octets.
func armELF(n int, fill byte) []byte {
	b := fakeBinary(n, fill)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0x28, 0})
	return b
}

// qpBench : un « QIARA Plus » réel (qp-update du dépôt) dans des dossiers temporaires.
type qpBench struct {
	g     *rig
	paths qplus.Paths
	qp    string
	bulk  string
	run   string
}

func newQPBench(t *testing.T) *qpBench {
	t.Helper()
	tool, _ := filepath.Abs("../../../qiara-plus/dist/qp-update")
	if _, err := os.Stat(tool); err != nil {
		t.Skip("qiara-plus/dist/qp-update absent")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh absent")
	}
	root := t.TempDir()
	b := &qpBench{qp: filepath.Join(root, "data", "qp"), bulk: filepath.Join(root, "media", "qp"), run: filepath.Join(root, "tmp", "qp")}
	for _, d := range []string{b.qp + "/state", b.bulk, b.run} {
		os.MkdirAll(d, 0o755)
	}
	t.Setenv("QP", b.qp)
	t.Setenv("QP_BULK", b.bulk)
	t.Setenv("QP_RUN", b.run)
	t.Setenv("QP_LOG", filepath.Join(root, "log"))
	t.Setenv("QP_OLD", filepath.Join(root, "old"))
	t.Setenv("QP_NO_OLD", "1")
	data, _ := os.ReadFile(tool)
	os.WriteFile(b.qp+"/qp-update", data, 0o755)
	os.WriteFile(b.qp+"/qp-guard", []byte("#!/bin/sh\n"), 0o755)
	proc := filepath.Join(root, "proc")
	os.MkdirAll(proc, 0o755)
	os.WriteFile(proc+"/uptime", []byte("1000.00 900.00\n"), 0o644)
	os.WriteFile(b.run+"/guard_beat", []byte("990\n"), 0o644)
	b.paths = qplus.Paths{Run: b.run, State: b.qp + "/state", Guard: b.qp + "/qp-guard", Proc: proc}

	// binaire de référence en good, comme après l'installation de QIARA Plus
	good := filepath.Join(root, "good.bin")
	os.WriteFile(good, armELF(1_500_000, 5), 0o644)
	if out, err := exec.Command("sh", b.qp+"/qp-update", "seed-good", good).CombinedOutput(); err != nil {
		t.Fatalf("seed-good : %v %s", err, out)
	}

	g := newRig(t)
	sys := NewSystem(SystemConfig{Paths: b.paths, Incoming: g.u.IncomingDir, Settings: g.u.Settings, Disarmed: g.u.Disarmed})
	g.u.Ready, g.u.Trial, g.u.Stage, g.u.Restart, g.u.Rollback, g.u.StatePath = sys.Ready, sys.Trial, sys.Stage, sys.Restart, sys.Rollback, sys.StatePath
	b.g = g
	return b
}

func (b *qpBench) slotNew() string { return filepath.Join(b.bulk, "slots", "new", "openqiarad") }

func TestRealQPUpdateStageAndOrder(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	bin := armELF(1_200_000, 1)
	m := g.rel.publish(5, bin, nil)
	if got := g.check(t); got != ResInstalling {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	// le binaire est dans new, l'essai est posé, l'ordre de relance est laissé au gardien
	got, err := os.ReadFile(b.slotNew())
	if err != nil || len(got) != len(bin) {
		t.Fatalf("new : %v (%d octets)", err, len(got))
	}
	if !b.paths.Trial() {
		t.Error("aucun essai posé")
	}
	if _, err := os.Stat(b.run + "/cmd/restart-app"); err != nil {
		t.Errorf("ordre restart-app absent : %v", err)
	}
	if !strings.Contains(readFile(b.qp+"/state/trial"), m.SHA256) {
		t.Errorf("l'essai ne porte pas la somme signée : %q", readFile(b.qp+"/state/trial"))
	}
	g.incomingEmpty(t)
	// l'essai en cours bloque toute autre installation
	g.rel.publish(6, armELF(1_200_000, 2), nil)
	n := g.rel.count("/openqiarad-test")
	g.u.chk.Lock()
	g.u.st.PendingSeq = 0
	g.u.chk.Unlock()
	if got := g.check(t); got != ResWaiting || g.rel.count("/openqiarad-test") != n {
		t.Errorf("pendant l'essai : %q", got)
	}
}

func TestRealQPUpdateRefusesNonELF(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	g.rel.publish(5, fakeBinaryNoELF(1_200_000), nil) // signé, somme juste, mais pas un exécutable ARM
	if got := g.check(t); got != ResError || !strings.Contains(g.u.Status().Message, "ELF") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if _, err := os.Stat(b.slotNew()); err == nil {
		t.Error("un fichier non ELF est arrivé dans new")
	}
	if b.paths.Trial() || g.restarts != 0 {
		t.Error("essai posé pour un fichier refusé")
	}
}

func TestRealQPUpdateRollbackWhenGuardGone(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	g.rel.publish(5, armELF(1_200_000, 1), nil)
	os.WriteFile(b.run+"/guard_beat", []byte("100\n"), 0o644) // le gardien ne bat plus depuis 15 minutes
	if got := g.check(t); got != ResWaiting {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if g.rel.count("/openqiarad-test") != 0 || b.paths.Trial() {
		t.Error("geste malgré l'absence du gardien")
	}
}

func TestRealQPUpdateUndoAfterRestartFailure(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	g.rel.publish(5, armELF(1_200_000, 1), nil)
	g.u.Restart = func() error { return qplus.ErrGuardAbsent } // le gardien disparaît entre le dépôt et l'ordre
	if got := g.check(t); got != ResWaiting {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if b.paths.Trial() {
		t.Error("l'essai déposé n'a pas été annulé")
	}
	if a := strings.TrimSpace(readFile(b.qp + "/state/active")); a != "good" {
		t.Errorf("emplacement actif %q, good attendu", a)
	}
}

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

func fakeBinaryNoELF(n int) []byte {
	b := make([]byte, n)
	copy(b, "#!/bin/sh\n")
	for i := 20; i < n; i++ {
		b[i] = byte(i % 251)
	}
	return b
}

// Revue V17, avec le vrai qp-update : l'alarme s'arme pendant que l'ordre de relance attend le
// surveillant. L'ordre est retiré de /tmp/qp/cmd et l'essai annulé (emplacement good actif).
func TestRealQPUpdateOrderWithdrawnWhenArmed(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	sys := NewSystem(SystemConfig{Paths: b.paths, Incoming: g.u.IncomingDir, Settings: g.u.Settings})
	g.u.OrderWaiting, g.u.WithdrawOrder = sys.OrderWaiting, sys.WithdrawOrder
	g.u.PollEvery = 5 * time.Millisecond
	calls := 0
	g.u.Disarmed = func() bool { calls++; return calls <= 3 }
	g.rel.publish(5, armELF(1_200_000, 1), nil)
	if got := g.check(t); got != ResWaiting {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if _, err := os.Stat(b.run + "/cmd/restart-app"); err == nil {
		t.Error("ordre de relance laissé au surveillant alors que l'alarme est armée")
	}
	if b.paths.Trial() {
		t.Error("essai toujours posé")
	}
	if a := strings.TrimSpace(readFile(b.qp + "/state/active")); a != "good" {
		t.Errorf("emplacement actif %q, good attendu", a)
	}
}

// Revue V17 : QIARA Plus sans qp-update (installation incomplète, version ancienne) : rien n'est
// téléchargé (sur claude17 : 12 Mo téléchargés à chaque vérification, puis dépôt refusé).
func TestRealQPUpdateToolMissing(t *testing.T) {
	b := newQPBench(t)
	g := b.g
	os.Remove(b.qp + "/qp-update")
	g.rel.publish(5, armELF(1_200_000, 1), nil)
	if got := g.check(t); got != ResWaiting || !strings.Contains(g.u.Status().Message, "qp-update absent") {
		t.Fatalf("résultat %q (%s)", got, g.u.Status().Message)
	}
	if g.rel.count("/openqiarad-test") != 0 {
		t.Error("binaire téléchargé alors que qp-update manque")
	}
}

// Revue V17 : les réglages d'essai de qp-update qui contournent ses contrôles ne lui sont jamais
// transmis (sur claude17 l'environnement du daemon passait tel quel, QP_FORCE compris) ; les chemins
// (QP, QP_BULK…) le sont.
func TestRunToolEnvironmentSanitized(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "outil.sh")
	os.WriteFile(script, []byte(`echo "force=[$QP_FORCE] free=[$QP_FAKE_FREE] link=[$QP_LINK] max=[$QP_MAX_SIZE] qp=[$QP]" >&2; exit 1`+"\n"), 0o644)
	t.Setenv("QP_FORCE", "1")
	t.Setenv("QP_FAKE_FREE", "999999")
	t.Setenv("QP_LINK", "1")
	t.Setenv("QP_MAX_SIZE", "99999999999")
	t.Setenv("QP", "/x/qp")
	err := runTool(context.Background(), script)
	if err == nil || err.Error() != "force=[] free=[] link=[] max=[] qp=[/x/qp]" {
		t.Fatalf("environnement transmis à qp-update : %v", err)
	}
}

// La sortie gardée est bornée (4 Kio) et le message en est la fin, en UTF-8 valide.
func TestRunToolOutputBounded(t *testing.T) {
	tb := &tailBuffer{max: 8}
	tb.Write([]byte("abc"))
	tb.Write([]byte("defghij"))
	if tb.String() != "cdefghij" {
		t.Errorf("fin gardée %q", tb.String())
	}
	tb.Write([]byte("0123456789XYZ"))
	if tb.String() != "56789XYZ" {
		t.Errorf("fin gardée %q", tb.String())
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "bavard.sh")
	os.WriteFile(script, []byte("i=0; while [ $i -lt 2000 ]; do echo 'ligne de remplissage assez longue pour dépasser la borne'; i=$((i+1)); done; echo 'ERREUR: fin' >&2; exit 1\n"), 0o644)
	err := runTool(context.Background(), script)
	if err == nil || !strings.HasSuffix(err.Error(), "ERREUR: fin") || len(err.Error()) > 300 {
		t.Fatalf("message %v", err)
	}
}
