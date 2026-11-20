//go:build linux

package sigupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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
			if !p.GuardBeating() {
				return qplus.ErrGuardAbsent
			}
			return nil
		},
		Trial: p.Trial,
		Stage: func(ctx context.Context, file, sha string) error {
			return runTool(ctx, p.UpdateTool(), "stage", file, sha)
		},
		Restart: func() error { return p.Order("restart-app") },
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

// runTool lance « sh qp-update args… » ; en cas d'échec, la fin de sa sortie sert de message.
func runTool(ctx context.Context, tool string, args ...string) error {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", append([]string{tool}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", oneLine(msg))
	}
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
