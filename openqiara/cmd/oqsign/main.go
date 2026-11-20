// oqsign : outil de signature des mises à jour d'OpenQIARA Claude, à lancer SUR LE POSTE DE
// VINCENT, jamais sur la caméra ni dans une conversation avec Claude.
//
//	oqsign keygen -out cle-privee.oqkey        crée la paire ; la clé privée va dans le fichier (droits 0600),
//	                                           seule la clé publique est affichée
//	oqsign pubkey -key cle-privee.oqkey        réaffiche la clé publique
//	oqsign sign   -key cle-privee.oqkey -in openqiarad-claude-v17 -version V -seq N [-channel stable]
//	              [-min-seq M] [-asset NOM] [-outdir DIR]    écrit update.json et update.json.sig
//	oqsign verify -pub CLE|FICHIER -manifest update.json [-sig update.json.sig] [-bin BINAIRE]
//
// La clé privée n'est jamais affichée, jamais passée en argument de ligne de commande (un argument
// se voit dans la liste des processus) : elle n'existe que dans son fichier.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caligone/openqiara/internal/sigupdate"
)

const keyHeader = "openqiara-claude-update-key-v1"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = cmdKeygen(args[1:], stdout)
	case "pubkey":
		err = cmdPubkey(args[1:], stdout)
	case "sign":
		err = cmdSign(args[1:], stdout)
	case "verify":
		err = cmdVerify(args[1:], stdout)
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "oqsign : commande inconnue %q\n", args[0])
		usage(stderr)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "oqsign :", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage :
  oqsign keygen -out FICHIER_CLE_PRIVEE
  oqsign pubkey -key FICHIER_CLE_PRIVEE
  oqsign sign   -key FICHIER_CLE_PRIVEE -in BINAIRE -version TEXTE -seq N [-channel stable] [-min-seq M] [-asset NOM] [-outdir DIR]
  oqsign verify -pub CLE_PUBLIQUE|FICHIER -manifest update.json [-sig update.json.sig] [-bin BINAIRE]
`)
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

func cmdKeygen(args []string, out io.Writer) error {
	fs := newFlags("keygen")
	path := fs.String("out", "", "fichier où écrire la clé privée (créé, jamais écrasé)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("keygen : -out FICHIER obligatoire")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	// O_EXCL : jamais d'écrasement d'une clé existante ; 0600 dès la création (pas de fenêtre lisible).
	f, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("keygen : %w (un fichier de clé existant n'est jamais écrasé)", err)
	}
	body := keyHeader + "\n" + base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"
	_, werr := f.WriteString(body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(*path)
		return fmt.Errorf("keygen : écriture impossible : %v %v", werr, cerr)
	}
	fmt.Fprintf(out, "Clé privée écrite dans %s (droits 0600). Elle n'est affichée nulle part : la sauvegarder hors ligne, ne la partager avec personne.\n", *path)
	fmt.Fprintf(out, "Clé publique (à graver dans internal/sigupdate/keys.go, ProductionKeys) :\n%s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Fprintf(out, "Empreinte : %s\n", fingerprint(pub))
	return nil
}

// loadPrivate lit la clé privée. Refuse un fichier lisible par d'autres, comme ssh.
func loadPrivate(path string) (ed25519.PrivateKey, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s n'est pas un fichier ordinaire", path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s est lisible par d'autres utilisateurs (droits %o) : chmod 600 d'abord", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(string(b))
	if len(lines) != 2 || lines[0] != keyHeader {
		return nil, errors.New("fichier de clé privée illisible (format inattendu)")
	}
	seed, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("fichier de clé privée illisible (graine invalide)")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func cmdPubkey(args []string, out io.Writer) error {
	fs := newFlags("pubkey")
	path := fs.String("key", "", "fichier de clé privée")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("pubkey : -key FICHIER obligatoire")
	}
	priv, err := loadPrivate(*path)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	fmt.Fprintf(out, "%s\nEmpreinte : %s\n", base64.StdEncoding.EncodeToString(pub), fingerprint(pub))
	return nil
}

// checkARM32 : en-tête d'un ELF 32 bits ARM petit-boutiste, comme qp-update.
func checkARM32(b []byte) error {
	if len(b) < 20 || !bytes.HasPrefix(b, []byte{0x7f, 'E', 'L', 'F'}) {
		return errors.New("le fichier n'est pas un exécutable ELF")
	}
	if b[4] != 1 || b[5] != 1 {
		return errors.New("ELF non 32 bits petit-boutiste")
	}
	if b[18] != 0x28 || b[19] != 0 {
		return errors.New("ELF non ARM")
	}
	return nil
}

func cmdSign(args []string, out io.Writer) error {
	fs := newFlags("sign")
	key := fs.String("key", "", "fichier de clé privée")
	in := fs.String("in", "", "binaire à publier (ELF ARM 32 bits)")
	version := fs.String("version", "", "texte de la version, par exemple v0.1.0-rc.23-claude-v17")
	seq := fs.Int64("seq", 0, "numéro de la version signée (>= 1), celui compilé dans le binaire (build-arm.sh ... seq)")
	channel := fs.String("channel", "stable", "canal")
	minSeq := fs.Int64("min-seq", 0, "plus petit seq installé qui peut passer directement à celui-ci")
	asset := fs.String("asset", "", "nom du fichier dans la release (défaut : nom du binaire)")
	outdir := fs.String("outdir", ".", "dossier où écrire update.json et update.json.sig")
	issued := fs.String("issued-at", "", "date RFC 3339 (défaut : maintenant)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{"-key": *key, "-in": *in, "-version": *version} {
		if v == "" {
			return fmt.Errorf("sign : %s obligatoire", name)
		}
	}
	if *seq < 1 {
		return errors.New("sign : -seq N obligatoire (entier >= 1)")
	}
	priv, err := loadPrivate(*key)
	if err != nil {
		return err
	}
	bin, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	if err := checkARM32(bin); err != nil {
		return fmt.Errorf("sign : %s : %v", *in, err)
	}
	// Le seq du manifeste doit être celui que le binaire porte : sinon la caméra ne reconnaîtrait pas
	// la version installée.
	emb, ok := sigupdate.FindSeqInBinary(bin)
	switch {
	case !ok:
		return errors.New("sign : ce binaire ne porte aucun seq (compilé sans -X main.signedSeq) : le reconstruire avec scripts/build-arm.sh <version> <date> <sortie> <seq>")
	case emb != *seq:
		return fmt.Errorf("sign : le binaire porte le seq %d, la commande demande %d : refusé", emb, *seq)
	}
	sum := sha256.Sum256(bin)
	name := *asset
	if name == "" {
		name = filepath.Base(*in)
	}
	when := *issued
	if when == "" {
		when = time.Now().UTC().Format(time.RFC3339)
	}
	m := sigupdate.Manifest{Channel: *channel, Seq: *seq, Version: *version, IssuedAt: when, Asset: name,
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin)), MinSeq: *minSeq}
	if err := m.Validate(); err != nil {
		return err
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mb = append(mb, '\n')
	sig := ed25519.Sign(priv, sigupdate.SignedBytes(mb))
	// Contrôle avant d'écrire : ce qui sort doit être accepté par le code de la caméra.
	pub := priv.Public().(ed25519.PublicKey)
	if _, err := sigupdate.Verify([]ed25519.PublicKey{pub}, mb, sigupdate.EncodeSignature(sig)); err != nil {
		return fmt.Errorf("sign : contrôle interne échoué : %w", err)
	}
	mfile := filepath.Join(*outdir, sigupdate.ManifestName(m.Channel))
	if err := os.MkdirAll(*outdir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(mfile, mb, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(mfile+".sig", sigupdate.EncodeSignature(sig), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Écrit : %s et %s.sig\n", mfile, mfile)
	fmt.Fprintf(out, "À joindre à la release GitHub avec le binaire « %s » (%d octets, sha256 %s).\n", name, m.Size, m.SHA256)
	fmt.Fprintf(out, "Version %q, seq %d, canal %s, signée par la clé %s.\n", m.Version, m.Seq, m.Channel, fingerprint(pub))
	return nil
}

func loadPublic(arg string) (ed25519.PublicKey, error) {
	if b, err := os.ReadFile(arg); err == nil {
		arg = string(b)
	}
	return sigupdate.ParsePublicKey(arg)
}

func cmdVerify(args []string, out io.Writer) error {
	fs := newFlags("verify")
	pubArg := fs.String("pub", "", "clé publique (base64 ou hexadécimal) ou fichier qui la contient")
	mfile := fs.String("manifest", "", "update.json")
	sfile := fs.String("sig", "", "signature (défaut : <manifeste>.sig)")
	binf := fs.String("bin", "", "binaire à comparer au manifeste (somme, taille, seq)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubArg == "" || *mfile == "" {
		return errors.New("verify : -pub et -manifest obligatoires")
	}
	pub, err := loadPublic(*pubArg)
	if err != nil {
		return err
	}
	if *sfile == "" {
		*sfile = *mfile + ".sig"
	}
	mb, err := os.ReadFile(*mfile)
	if err != nil {
		return err
	}
	sb, err := os.ReadFile(*sfile)
	if err != nil {
		return err
	}
	m, err := sigupdate.Verify([]ed25519.PublicKey{pub}, mb, sb)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Signature valide (clé %s) : %q, seq %d, canal %s, %s, %d octets.\n", fingerprint(pub), m.Version, m.Seq, m.Channel, m.Asset, m.Size)
	if *binf != "" {
		bin, err := os.ReadFile(*binf)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(bin)
		if int64(len(bin)) != m.Size || hex.EncodeToString(sum[:]) != m.SHA256 {
			return errors.New("verify : le binaire ne correspond pas au manifeste (taille ou somme SHA-256)")
		}
		if err := checkARM32(bin); err != nil {
			return fmt.Errorf("verify : %v", err)
		}
		emb, ok := sigupdate.FindSeqInBinary(bin)
		if !ok || emb != m.Seq {
			return fmt.Errorf("verify : le seq du binaire (%d, trouvé : %v) diffère de celui du manifeste (%d)", emb, ok, m.Seq)
		}
		fmt.Fprintln(out, "Le binaire correspond au manifeste (somme, taille, seq).")
	}
	return nil
}
