package sigupdate

import (
	"bytes"
	"strconv"
)

// Le seq d'une version signée est écrit dans le binaire à la compilation
// (-X main.signedSeq=OQSEQ=17;) pour que oqsign puisse vérifier, avant de signer, que le
// manifeste annonce bien le seq du binaire : sinon la version installée ne se reconnaîtrait
// pas (elle serait comptée comme revenue en arrière) et la suivante serait mal ordonnée.
//
// Le préfixe est composé à l'exécution (octet par octet) : aucune chaîne « OQSEQ= » ne figure telle
// quelle dans le binaire, la seule occurrence complète est celle de l'option -X. Un binaire compilé
// sans -X n'a pas de marqueur : seq 0, non signable.
var seqPrefix = string([]byte{'O', 'Q', 'S', 'E', 'Q', '='})

const seqSuffix = ";"

// SeqMarker écrit le marqueur d'un seq (valeur de -X main.signedSeq).
func SeqMarker(seq int64) string { return seqPrefix + strconv.FormatInt(seq, 10) + seqSuffix }

// ParseSeqMarker lit « OQSEQ=17; » ; faux si la forme n'est pas exactement celle-là.
func ParseSeqMarker(s string) (int64, bool) {
	if len(s) < len(seqPrefix)+len(seqSuffix)+1 || s[:len(seqPrefix)] != seqPrefix || s[len(s)-1:] != seqSuffix {
		return 0, false
	}
	n, err := strconv.ParseInt(s[len(seqPrefix):len(s)-1], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// FindSeqInBinary cherche le marqueur dans un binaire compilé. Le code ne contient que le préfixe
// et le suffixe séparés (jamais la forme complète) : la seule occurrence complète est celle de
// l'option -X. Plusieurs occurrences différentes : faux (ambigu).
func FindSeqInBinary(bin []byte) (int64, bool) {
	var found int64 = -1
	rest := bin
	for {
		i := bytes.Index(rest, []byte(seqPrefix))
		if i < 0 {
			break
		}
		rest = rest[i+len(seqPrefix):]
		j := 0
		for j < len(rest) && j < 19 && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 || j >= len(rest) || rest[j] != seqSuffix[0] {
			continue
		}
		n, err := strconv.ParseInt(string(rest[:j]), 10, 64)
		if err != nil {
			continue
		}
		if found >= 0 && found != n {
			return 0, false
		}
		found = n
	}
	if found < 0 {
		return 0, false
	}
	return found, true
}
