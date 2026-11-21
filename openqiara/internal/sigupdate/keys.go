package sigupdate

import "crypto/ed25519"

// ProductionKeys : clés publiques Ed25519 (base64, 32 octets) dont une signature
// autorise l'installation. VIDE dans les sources tant que Vincent n'a pas créé sa
// paire de clés (oqsign keygen) : la mise à jour signée reste alors inerte,
// quels que soient les réglages. Pour une rotation, graver l'ancienne et la
// nouvelle clé ensemble le temps d'une version, puis retirer l'ancienne.
//
// La clé PRIVÉE n'est jamais dans le dépôt, ni sur la caméra.
var ProductionKeys = []string{}

// Keys renvoie les clés de production analysées. Une entrée illisible est une
// erreur de programmation : elle est ignorée ici (aucune clé de moins à confiance)
// et signalée par le test TestProductionKeysParse.
func Keys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, s := range ProductionKeys {
		if k, err := ParsePublicKey(s); err == nil {
			out = append(out, k)
		}
	}
	return out
}
