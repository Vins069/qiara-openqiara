package sigupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Bornes de rythme, appliquées quels que soient les réglages.
const (
	MinInterval     = 6 * time.Hour
	DefaultInterval = 12 * time.Hour
	MaxInterval     = 7 * 24 * time.Hour
	FirstDelay      = 15 * time.Minute // laisse passer l'essai d'une version installée (180 s) avant de relire
	reserveBytes    = 16 << 20
)

// RepoRe : « propriétaire/dépôt » GitHub.
var RepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// Settings : réglages lus à chaque vérification (modifiables sans redémarrage).
type Settings struct {
	Enabled bool
	Repo    string // propriétaire/dépôt
	Channel string // « stable » si vide
	Every   time.Duration
}

// Updater est la mise à jour signée. Tous les accès au système passent par des
// fonctions : les tests les remplacent, system.go branche les vraies.
type Updater struct {
	Keys        []ed25519.PublicKey
	HTTP        *http.Client
	BaseURL     func(repo string) string // se termine par « / » ; défaut : releases/latest/download de GitHub
	RunningSeq  int64                    // seq du binaire qui tourne (0 : binaire non signé, par exemple la V16)
	StatePath   string
	IncomingDir string

	Settings  func() Settings
	Disarmed  func() bool  // alarme désarmée ET état connu
	Ready     func() error // QIARA Plus présent et gardien vivant
	Trial     func() bool  // un essai de QIARA Plus est en cours
	Stage     func(ctx context.Context, file, sha string) error
	Restart   func() error                    // ordre restart-app
	Rollback  func(ctx context.Context) error // annule l'essai déposé (rien à annuler : sans erreur)
	FreeSpace func(dir string) (uint64, error)
	Now       func() time.Time
	Log       *slog.Logger

	First           time.Duration
	ManifestTimeout time.Duration
	DownloadTimeout time.Duration

	mu     sync.Mutex
	st     State
	loaded bool
}

// Status : l'état exposé par l'API, le diagnostic QIARA Plus et MQTT.
type Status struct {
	Active      bool   `json:"active"` // activée ET au moins une clé gravée
	Keys        int    `json:"keys"`
	RunningSeq  int64  `json:"running_seq"`
	LastCheck   string `json:"last_check,omitempty"`
	Result      string `json:"result,omitempty"`
	Message     string `json:"message,omitempty"`
	SeenSeq     int64  `json:"seen_seq,omitempty"`
	SeenVersion string `json:"seen_version,omitempty"`
	PendingSeq  int64  `json:"pending_seq,omitempty"`
	FailedSeq   int64  `json:"failed_seq,omitempty"`
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) logger() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.Default()
}

func (u *Updater) load() {
	if !u.loaded {
		u.st = loadState(u.StatePath)
		u.loaded = true
	}
}

// Status renvoie l'état courant.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.load()
	s := Status{Keys: len(u.Keys), RunningSeq: u.RunningSeq, Result: u.st.Result, Message: u.st.Message,
		SeenSeq: u.st.SeenSeq, SeenVersion: u.st.SeenVersion, PendingSeq: u.st.PendingSeq, FailedSeq: u.st.FailedSeq}
	if !u.st.LastCheck.IsZero() {
		s.LastCheck = u.st.LastCheck.UTC().Format(time.RFC3339)
	}
	s.Active = len(u.Keys) > 0 && u.Settings != nil && u.Settings().Enabled
	return s
}

// Run vérifie périodiquement. Bloque jusqu'à l'annulation de ctx. Au démarrage,
// rapproche aussitôt l'état gardé de ce qui tourne (version installée, ou retour arrière).
func (u *Updater) Run(ctx context.Context) {
	u.mu.Lock()
	u.load()
	u.reconcile()
	u.mu.Unlock()
	wait := u.First
	if wait <= 0 {
		wait = FirstDelay
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s := u.Settings()
		wait = ClampInterval(s.Every)
		if !s.Enabled {
			continue
		}
		u.CheckOnce(ctx)
	}
}

// ClampInterval : 12 h par défaut, 6 h au minimum, 7 jours au maximum.
func ClampInterval(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultInterval
	case d < MinInterval:
		return MinInterval
	case d > MaxInterval:
		return MaxInterval
	}
	return d
}

// note enregistre le résultat d'une vérification. Appelé verrou pris.
func (u *Updater) note(code, msg string, checked bool) string {
	if checked {
		u.st.LastCheck = u.now()
	}
	u.st.Result, u.st.Message = code, msg
	if err := saveState(u.StatePath, u.st); err != nil {
		u.logger().Warn("mise à jour signée : état non enregistré", "error", err)
	}
	lvl := slog.LevelInfo
	if code == ResError || code == ResSignature || code == ResRolledBack {
		lvl = slog.LevelWarn
	}
	u.logger().Log(context.Background(), lvl, "mise à jour signée", "résultat", code, "message", msg)
	return code
}

// reconcile rapproche l'installation en cours de ce qui tourne. Verrou pris.
func (u *Updater) reconcile() {
	p := u.st.PendingSeq
	if p == 0 {
		return
	}
	trial := u.Trial != nil && u.Trial()
	switch {
	case u.RunningSeq >= p && !trial:
		u.st.PendingSeq, u.st.PendingSHA, u.st.PendingAt = 0, "", time.Time{}
		u.note(ResInstalled, fmt.Sprintf("version signée %d en service", u.RunningSeq), false)
	case trial:
		u.note(ResInstalling, fmt.Sprintf("version signée %d : essai de QIARA Plus en cours", p), false)
	default: // elle ne tourne pas et plus aucun essai : QIARA Plus est revenu en arrière
		if p > u.st.FailedSeq {
			u.st.FailedSeq = p
		}
		u.st.PendingSeq, u.st.PendingSHA, u.st.PendingAt = 0, "", time.Time{}
		u.note(ResRolledBack, fmt.Sprintf("la version signée %d n'a pas passé l'essai : QIARA Plus est revenu en arrière, elle ne sera pas réessayée", p), false)
	}
}

// CheckOnce fait une vérification complète et renvoie son code de résultat.
func (u *Updater) CheckOnce(ctx context.Context) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.load()
	u.reconcile()
	set := u.Settings()
	if len(u.Keys) == 0 {
		return u.note(ResNoKey, ErrNoKeys.Error(), false)
	}
	if !set.Enabled {
		return u.note(ResDisabled, "option update.signed.enabled désactivée", false)
	}
	if !RepoRe.MatchString(set.Repo) {
		return u.note(ResError, "dépôt non renseigné ou invalide (update.signed.repo, forme « propriétaire/dépôt »)", true)
	}
	channel := set.Channel
	if channel == "" {
		channel = "stable"
	}
	if !channelRe.MatchString(channel) {
		return u.note(ResError, "canal invalide", true)
	}
	if u.st.PendingSeq != 0 {
		return u.note(ResInstalling, fmt.Sprintf("version signée %d en cours d'essai : rien d'autre avant la fin", u.st.PendingSeq), true)
	}

	base := "https://github.com/" + set.Repo + "/releases/latest/download/"
	if u.BaseURL != nil {
		base = u.BaseURL(set.Repo)
	}
	name := ManifestName(channel)
	mctx, cancel := context.WithTimeout(ctx, orDur(u.ManifestTimeout, 30*time.Second))
	var mb, sb bytes.Buffer
	err := u.fetch(mctx, base+name, maxManifest, &mb)
	if err == nil {
		err = u.fetch(mctx, base+name+".sig", maxSignature, &sb)
	}
	cancel()
	if err != nil {
		return u.note(ResNetwork, "lecture du manifeste impossible : "+err.Error(), true)
	}
	m, err := Verify(u.Keys, mb.Bytes(), sb.Bytes())
	if err != nil {
		return u.note(ResSignature, err.Error(), true)
	}

	// Décision, avant tout téléchargement.
	if m.Channel != channel {
		return u.note(ResRefused, fmt.Sprintf("manifeste du canal %q, canal réglé %q", m.Channel, channel), true)
	}
	if m.Seq < u.st.HighSeq {
		return u.note(ResRefused, fmt.Sprintf("manifeste plus ancien (seq %d) que celui déjà vu (seq %d) : retour arrière refusé", m.Seq, u.st.HighSeq), true)
	}
	u.st.HighSeq, u.st.SeenSeq, u.st.SeenVersion = m.Seq, m.Seq, m.Version
	switch {
	case m.Seq <= u.RunningSeq:
		return u.note(ResUpToDate, fmt.Sprintf("à jour (version signée %d)", u.RunningSeq), true)
	case m.Seq <= u.st.FailedSeq:
		return u.note(ResRefused, fmt.Sprintf("la version signée %d est déjà revenue en arrière (seq ≤ %d) : une nouvelle version est nécessaire", m.Seq, u.st.FailedSeq), true)
	case m.MinSeq > u.RunningSeq:
		return u.note(ResRefused, fmt.Sprintf("la version signée %d exige d'abord la version %d (installée : %d)", m.Seq, m.MinSeq, u.RunningSeq), true)
	}

	// Garde-fous : jamais alarme armée ni état inconnu, jamais pendant un essai.
	if u.Disarmed == nil || !u.Disarmed() {
		return u.note(ResWaiting, "alarme armée ou état inconnu : installation reportée", true)
	}
	if u.Trial != nil && u.Trial() {
		return u.note(ResWaiting, "un essai de QIARA Plus est en cours : installation reportée", true)
	}
	if u.Ready != nil {
		if err := u.Ready(); err != nil {
			return u.note(ResWaiting, "QIARA Plus indisponible : "+err.Error(), true)
		}
	}
	if u.FreeSpace != nil {
		if free, err := u.FreeSpace(u.IncomingDir); err == nil && free < uint64(m.Size)+reserveBytes {
			return u.note(ResWaiting, fmt.Sprintf("place insuffisante (%d Mo libres)", free>>20), true)
		}
	}

	// Téléchargement borné, somme contrôlée au fil de l'eau.
	if err := os.MkdirAll(u.IncomingDir, 0o755); err != nil {
		return u.note(ResError, "dossier de dépôt : "+err.Error(), true)
	}
	u.cleanIncoming()
	final := filepath.Join(u.IncomingDir, fmt.Sprintf("sig-%d.bin", m.Seq))
	part := final + ".part"
	defer os.Remove(part)
	defer os.Remove(final)
	if err := u.download(ctx, base+m.Asset, m, part); err != nil {
		return u.note(ResNetwork, "téléchargement : "+err.Error(), true)
	}
	if err := os.Rename(part, final); err != nil {
		return u.note(ResError, "dépôt du binaire : "+err.Error(), true)
	}

	// Dernier contrôle de l'alarme juste avant le geste, puis dépôt chez QIARA Plus.
	if !u.Disarmed() {
		return u.note(ResWaiting, "alarme armée pendant le téléchargement : installation reportée", true)
	}
	sctx, scancel := context.WithTimeout(ctx, 3*time.Minute)
	err = u.Stage(sctx, final, m.SHA256)
	scancel()
	if err != nil {
		return u.note(ResError, "dépôt chez QIARA Plus refusé : "+err.Error(), true)
	}
	if !u.Disarmed() { // l'essai est déposé mais l'application n'a pas été relancée : on l'annule
		u.undo(ctx)
		return u.note(ResWaiting, "alarme armée au dernier moment : essai annulé, installation reportée", true)
	}
	u.st.PendingSeq, u.st.PendingSHA, u.st.PendingAt = m.Seq, m.SHA256, u.now()
	if err := saveState(u.StatePath, u.st); err != nil {
		u.logger().Warn("mise à jour signée : état non enregistré", "error", err)
	}
	if err := u.Restart(); err != nil {
		u.undo(ctx)
		u.st.PendingSeq, u.st.PendingSHA, u.st.PendingAt = 0, "", time.Time{}
		return u.note(ResWaiting, "relance impossible ("+err.Error()+") : essai annulé, nouvel essai à la prochaine vérification", true)
	}
	return u.note(ResInstalling, fmt.Sprintf("version %s (seq %d) déposée chez QIARA Plus, relance demandée", m.Version, m.Seq), true)
}

func (u *Updater) undo(ctx context.Context) {
	if u.Rollback == nil {
		return
	}
	rctx, c := context.WithTimeout(ctx, time.Minute)
	defer c()
	if err := u.Rollback(rctx); err != nil {
		u.logger().Error("mise à jour signée : annulation de l'essai impossible", "error", err)
	}
}

func (u *Updater) cleanIncoming() {
	ents, _ := os.ReadDir(u.IncomingDir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "sig-") {
			os.Remove(filepath.Join(u.IncomingDir, e.Name()))
		}
	}
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

var errTooBig = errors.New("réponse plus grosse que la limite")

// fetch lit une adresse en HTTPS dans w, au plus limit octets.
func (u *Updater) fetch(ctx context.Context, rawURL string, limit int64, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "openqiara-claude-sigupdate")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("réponse HTTP %d pour %s", resp.StatusCode, shortURL(rawURL))
	}
	if resp.ContentLength > limit {
		return errTooBig
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errTooBig
	}
	return nil
}

func shortURL(s string) string {
	if p, err := url.Parse(s); err == nil {
		return p.Host + p.Path
	}
	return "(adresse)"
}

// download écrit l'asset dans part et contrôle taille et somme SHA-256 du manifeste signé.
func (u *Updater) download(ctx context.Context, rawURL string, m *Manifest, part string) error {
	dctx, cancel := context.WithTimeout(ctx, orDur(u.DownloadTimeout, 10*time.Minute))
	defer cancel()
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	err = u.fetch(dctx, rawURL, m.Size, cw)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if cw.n != m.Size {
		return fmt.Errorf("taille reçue %d, signée %d", cw.n, m.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		return fmt.Errorf("somme SHA-256 différente de celle signée (%s…)", got[:12])
	}
	return nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// RedirectPolicy : au plus 5 redirections, en HTTPS, vers des hôtes autorisés.
func RedirectPolicy(allow func(host string) bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("trop de redirections")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirection vers %q refusée (HTTPS seulement)", req.URL.Scheme)
		}
		if !allow(strings.ToLower(req.URL.Hostname())) {
			return fmt.Errorf("redirection vers %s refusée", req.URL.Hostname())
		}
		return nil
	}
}

// GitHubHost : github.com et les domaines qui servent ses fichiers de release.
func GitHubHost(h string) bool {
	return h == "github.com" || strings.HasSuffix(h, ".github.com") || strings.HasSuffix(h, ".githubusercontent.com")
}
