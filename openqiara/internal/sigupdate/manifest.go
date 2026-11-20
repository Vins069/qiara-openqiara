// Package sigupdate : mise à jour automatique d'OpenQIARA Claude, signée.
//
// La caméra interroge le dépôt GitHub de la distribution (« latest release »),
// y lit un manifeste update.json et sa signature update.json.sig (Ed25519),
// télécharge le binaire qu'il désigne, contrôle sa somme SHA-256, puis le
// confie à QIARA Plus (qp-update stage, puis ordre restart-app). Rien n'est
// installé sans une signature valide d'une des clés gravées dans le binaire
// (keys.go), et l'installation elle-même (essai, validation par la santé,
// retour arrière) reste celle de QIARA Plus : ce paquet ne remplace jamais
// /data/openqiarad.
//
// Désactivée par défaut (signed_update.enabled) et sans effet tant qu'aucune
// clé de production n'est gravée.
package sigupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Bornes du binaire : celles de qp-update (QP_MIN_SIZE, QP_MAX_SIZE).
const (
	MinBinarySize = 1_000_000
	MaxBinarySize = 24_000_000
	maxManifest   = 16 << 10
	maxSignature  = 1 << 10
)

// SignatureContext est préfixé au manifeste avant signature : une signature
// d'un autre usage de la même clé ne peut pas valoir signature de mise à jour.
const SignatureContext = "openqiara-claude-update-v1\n"

// Manifest est le contenu signé de update.json.
type Manifest struct {
	Channel  string `json:"channel"`           // « stable » par défaut
	Seq      int64  `json:"seq"`               // strictement croissant, jamais réutilisé
	Version  string `json:"version"`           // texte affiché (ex. « v0.1.0-rc.25-claude17 »)
	IssuedAt string `json:"issued_at"`         // RFC 3339, informatif
	Asset    string `json:"asset"`             // nom du binaire dans la release
	SHA256   string `json:"sha256"`            // minuscules, 64 caractères
	Size     int64  `json:"size"`              // octets
	MinSeq   int64  `json:"min_seq,omitempty"` // plus petit seq installé qui peut passer directement à celui-ci
}

var (
	ErrNoKeys       = errors.New("sigupdate: aucune clé de publication gravée : mise à jour signée désactivée")
	ErrBadSignature = errors.New("sigupdate: signature invalide")
	ErrBadManifest  = errors.New("sigupdate: manifeste invalide")
)

var (
	assetRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	channelRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	shaRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParsePublicKey lit une clé publique Ed25519 en base64 (ou en hexadécimal).
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	var raw []byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
		raw = b
	} else if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		raw = b
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("clé publique Ed25519 attendue (%d octets)", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// EncodeSignature écrit une signature au format du fichier .sig : base64, une ligne.
func EncodeSignature(sig []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// SignedBytes est ce qui est réellement signé : le contexte puis le fichier update.json tel quel.
func SignedBytes(manifest []byte) []byte {
	return append([]byte(SignatureContext), manifest...)
}

// Verify contrôle la signature (fichier .sig) du manifeste (fichier update.json)
// avec chacune des clés, puis l'analyse. Aucune clé : ErrNoKeys.
func Verify(keys []ed25519.PublicKey, manifest, sigFile []byte) (*Manifest, error) {
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	if len(manifest) == 0 || len(manifest) > maxManifest || len(sigFile) == 0 || len(sigFile) > maxSignature {
		return nil, ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrBadSignature
	}
	msg := SignedBytes(manifest)
	ok := false
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, msg, sig) {
			ok = true
		}
	}
	if !ok {
		return nil, ErrBadSignature
	}
	return ParseManifest(manifest)
}

// ParseManifest analyse et valide un manifeste (la signature est déjà contrôlée
// par Verify ; oqsign l'utilise aussi avant de signer).
func ParseManifest(b []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: données après le manifeste", ErrBadManifest)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate vérifie les champs, qui servent à fabriquer des chemins et des
// adresses : aucune confiance dans leur forme.
func (m *Manifest) Validate() error {
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrBadManifest, fmt.Sprintf(f, a...)) }
	switch {
	case !channelRe.MatchString(m.Channel):
		return bad("canal %q", m.Channel)
	case m.Seq < 1:
		return bad("seq %d (>= 1 attendu)", m.Seq)
	case m.MinSeq < 0 || m.MinSeq > m.Seq:
		return bad("min_seq %d hors de [0, seq]", m.MinSeq)
	case m.Version == "" || len(m.Version) > 64 || strings.ContainsAny(m.Version, "\r\n\x00"):
		return bad("version")
	case !assetRe.MatchString(m.Asset):
		return bad("nom de fichier %q", m.Asset)
	case !shaRe.MatchString(m.SHA256):
		return bad("sha256")
	case m.Size < MinBinarySize || m.Size > MaxBinarySize:
		return bad("taille %d hors de [%d, %d]", m.Size, MinBinarySize, MaxBinarySize)
	}
	if m.IssuedAt != "" {
		if _, err := time.Parse(time.RFC3339, m.IssuedAt); err != nil {
			return bad("issued_at")
		}
	}
	return nil
}

// ManifestName est le nom du manifeste d'un canal : update.json pour « stable »,
// update-<canal>.json pour les autres ; la signature ajoute « .sig ».
func ManifestName(channel string) string {
	if channel == "" || channel == "stable" {
		return "update.json"
	}
	return "update-" + channel + ".json"
}
