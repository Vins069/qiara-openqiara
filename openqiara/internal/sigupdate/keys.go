package sigupdate

import "crypto/ed25519"

// ProductionKeys : clés publiques Ed25519 (base64, 32 octets) dont une signature
// autorise l'installation. Vide, la mise à jour signée reste inerte, quels que soient
// les réglages. Pour une rotation, graver l'ancienne et la nouvelle clé ensemble le
// temps d'une version, puis retirer l'ancienne. Une autre personne autorisée à signer
// a sa propre clé, ajoutée ici : une clé privée ne se partage jamais.
//
// La clé PRIVÉE n'est jamais dans le dépôt, ni sur la caméra.
var ProductionKeys = []string{
	// Vincent (Vins069) : créée par « oqsign keygen » sur son Beelink le 21/11/2026 à 12 h 56,
	// empreinte 5393667379ec8291 ; la clé privée y reste.
	"NiButuH7wrwMmKAWxOPdt27yLuYj3WNQPL8qMbrfMVI=",
}

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
