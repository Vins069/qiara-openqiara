//go:build linux

package sigupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/caligone/openqiara/internal/ota"
	"github.com/caligone/openqiara/internal/qplus"
)

// IncomingDir : dépôt des binaires téléchargés, sur la carte SD (/media, 2,8 Go),
// jamais dans /data (trop petit pour deux binaires).
const IncomingDir = "/media/qp/incoming"

// SystemConfig : ce que le daemon fournit à la mise à jour signée.
type SystemConfig struct {
	RunningSeq int64
	Paths      qplus.Paths
	Incoming   string // IncomingDir par défaut
	Settings   func() Settings
	Disarmed   func() bool
	Logger     *slog.Logger
}

// NewSystem branche l'Updater sur la caméra : clés gravées, HTTPS à autorités
// embarquées (redirections limitées à GitHub), QIARA Plus.
func NewSystem(c SystemConfig) *Updater {
	p := c.Paths
	inc := c.Incoming
	if inc == "" {
		inc = IncomingDir
	}
	httpc := ota.NewHTTPClient(0) // les délais sont portés par les contextes de l'Updater
	httpc.CheckRedirect = RedirectPolicy(GitHubHost)
	return &Updater{
		Keys:        Keys(),
		HTTP:        httpc,
		RunningSeq:  c.RunningSeq,
		StatePath:   p.UpdateStatePath(),
		IncomingDir: inc,
		Settings:    c.Settings,
		Disarmed:    c.Disarmed,
		Ready: func() error {
			if !p.Installed() {
				return errors.New("non installé")
			}
			// qp-update absent ou illisible : le téléchargement serait refusé au dépôt, toutes les 12 h.
			if st, err := os.Stat(p.UpdateTool()); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
				return errors.New("qp-update absent")
			}
			if !p.GuardBeating() {
				return qplus.ErrGuardAbsent
			}
			return nil
		},
		Trial: p.Trial,
		Stage: func(ctx context.Context, file, sha string) error {
			return runTool(ctx, p.UpdateTool(), "stage", file, sha)
		},
		Restart:       func() error { return p.Order("restart-app") },
		OrderWaiting:  func() bool { return p.OrderWaiting("restart-app") },
		WithdrawOrder: func() bool { return p.WithdrawOrder("restart-app") },
		Rollback: func(ctx context.Context) error {
			return runTool(ctx, p.UpdateTool(), "rollback", "mise à jour signée annulée")
		},
		FreeSpace: freeSpace,
		Log:       c.Logger,
	}
}

func freeSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// shellPath : le shell de la caméra (busybox), sans dépendre du PATH du daemon.
const shellPath = "/bin/sh"

// toolEnvDrop : réglages d'essai de qp-update qui contournent ses contrôles (essai en cours, place,
// bornes de taille, lien au lieu de copie). Jamais transmis, même présents dans l'environnement du daemon.
var toolEnvDrop = []string{"QP_FORCE", "QP_FAKE_FREE", "QP_LINK", "QP_MIN_SIZE", "QP_MAX_SIZE", "QP_SAFETY_KB"}

func toolEnv(env []string) []string {
	out := env[:0:0]
next:
	for _, kv := range env {
		for _, k := range toolEnvDrop {
			if strings.HasPrefix(kv, k+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

// tailBuffer garde les derniers max octets écrits : la fin de la sortie de qp-update sert de message,
// et une sortie démesurée ne doit pas remplir la mémoire de la caméra.
type tailBuffer struct {
	b   []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.max {
		p = p[len(p)-t.max:]
	}
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		copy(t.b, t.b[len(t.b)-t.max:])
		t.b = t.b[:t.max]
	}
	return n, nil
}

func (t *tailBuffer) String() string { return string(t.b) }

// runTool lance « /bin/sh qp-update args… » ; en cas d'échec, la fin de sa sortie sert de message.
func runTool(ctx context.Context, tool string, args ...string) error {
	out := &tailBuffer{max: 4 << 10}
	cmd := exec.CommandContext(ctx, shellPath, append([]string{tool}, args...)...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = toolEnv(os.Environ())
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", oneLine(strings.ToValidUTF8(msg, "")))
	}
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
