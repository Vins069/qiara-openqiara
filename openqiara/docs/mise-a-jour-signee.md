# Mise à jour signée (OpenQIARA Claude V17)

La caméra peut se mettre à jour toute seule depuis le dépôt GitHub de la distribution, **mais seulement avec une
version signée par le propriétaire du dépôt**, et c'est QIARA Plus qui l'installe (essai, validation par la santé,
retour arrière automatique). C'est l'alternative de la distribution à l'installation automatique de l'amont
(`PUT /api/v1/config/update {"auto":true}`), qui reste refusée (`409`) : une release de l'amont écraserait les
ajouts de la distribution et le lanceur de QIARA Plus.

**État de livraison : désactivée, et inerte tant qu'aucune clé de publication n'est gravée dans le binaire**
(`internal/sigupdate/keys.go`, liste vide dans les sources). Rien n'est lu sur Internet, rien n'est installé.

## Ce qui se passe

1. Toutes les 12 h par défaut (6 h au minimum, 7 jours au maximum), la première vérification 15 minutes après le
   démarrage, `openqiarad` lit dans la **dernière release** du dépôt réglé (`signed_update.repo`) deux petits fichiers :
   `update.json` et `update.json.sig` (pour un autre canal que `stable` : `update-<canal>.json`). Adresse :
   `https://github.com/<dépôt>/releases/latest/download/<fichier>`. Pas l'API GitHub (pas de limite de requêtes).
2. Il contrôle la signature Ed25519 avec les clés gravées dans le binaire, puis le manifeste. Signature fausse :
   arrêt, rien n'est téléchargé.
3. Il décide **avant de télécharger** : version plus récente que celle qui tourne (`seq`), canal identique,
   `min_seq` satisfait, version pas déjà revenue en arrière, manifeste pas plus ancien que le plus récent déjà vu.
4. Garde-fous : alarme **désarmée et état connu** (jamais alarme armée, jamais état inconnu), aucun essai de
   QIARA Plus en cours, QIARA Plus présent (`qp-guard` et `qp-update`) et son surveillant vivant, place
   suffisante sur la carte (deux fois la taille du binaire plus 16 Mio : le fichier téléchargé et la copie que
   `qp-update` fait dans `new`).
5. Il télécharge le binaire désigné dans `/media/qp/incoming` (carte SD, jamais `/data`), au plus la taille signée,
   HTTPS avec les autorités de certification embarquées, au plus 5 redirections, en HTTPS, limitées à
   `github.com`, `*.github.com` et `*.githubusercontent.com`, délai de 10 minutes. Taille et SHA-256 doivent être ceux du manifeste signé.
6. Il redemande l'état de l'alarme, puis lance `qp-update stage <fichier> <sha256>` : QIARA Plus contrôle que c'est un
   ELF ARM 32 bits de taille plausible, le place dans `new` et pose l'essai.
7. Il redemande l'alarme une dernière fois, puis laisse l'ordre `restart-app` au surveillant de QIARA Plus.
   Si l'alarme s'est armée entre-temps, ou si l'ordre ne peut pas être donné, l'essai déposé est **annulé**
   (`qp-update rollback`) : la caméra reste sur sa version. L'ordre est ensuite **suivi** : tant qu'il attend le
   surveillant (qui passe toutes les 10 s), l'alarme est contrôlée chaque seconde ; armée, ou ordre non pris en
   3 minutes, l'ordre est retiré et l'essai annulé. Pris, il doit arrêter `openqiarad` dans les 2 minutes ; sinon
   (limite de 6 relances par heure du surveillant, mode passif, surveillant trop ancien) l'essai est annulé : un essai
   déposé ne part jamais au prochain démarrage venu (plantage, coupure de courant), peut-être alarme armée. Seule
   reste la fenêtre de quelques secondes entre la prise de l'ordre et l'arrêt de l'application.
8. QIARA Plus relance l'application sur `new`, la met à l'essai (180 s) et la valide par la santé, ou revient à
   `good`. À la vérification suivante, `openqiarad` constate le résultat : version en service (`installée`) ou version
   revenue en arrière (mémorisée, **jamais réessayée** ; il faut publier un `seq` plus grand). De même, une version
   **quittée** après avoir été en service (bouton « Revenir à la version précédente » de QIARA Plus, réinstallation à
   la main d'une version plus ancienne, repli de QIARA Plus) est reconnue au démarrage de la version qui tourne
   (résultat `retour-arriere`) et n'est plus réinstallée automatiquement.

`openqiarad` ne remplace jamais `/data/openqiarad`, ne redémarre jamais la caméra et ne touche à aucun autre
emplacement de QIARA Plus.

## Fichiers publiés avec la release

| Fichier | Contenu |
|---|---|
| `openqiarad-claude-v17` (nom libre) | le binaire ARM 32 bits |
| `update.json` | manifeste signé (ci-dessous) |
| `update.json.sig` | signature en base64, une ligne |

```json
{
  "channel": "stable",
  "seq": 17,
  "version": "v0.1.0-rc.23-claude-v17",
  "issued_at": "2026-11-20T10:00:00Z",
  "asset": "openqiarad-claude-v17",
  "sha256": "…64 caractères hexadécimaux en minuscules…",
  "size": 11900000,
  "min_seq": 0
}
```

- `seq` : entier strictement croissant, **jamais réutilisé**, égal au `seq` compilé dans le binaire (voir plus bas).
  Une caméra n'installe que `seq` supérieur à celui qui tourne et à celui d'une version déjà revenue en arrière.
  La numérotation est **commune à tous les canaux** (la caméra retient le plus grand `seq` vu, quel que soit le
  canal) et **sans retour** : un `seq` publié par erreur trop grand (1000 au lieu de 18) fait refuser tous les
  suivants jusqu'à 1001.
- `min_seq` : plus petit `seq` installé pour qu'une mise à jour directe soit permise (0 : aucune exigence). Sert à
  imposer un passage par une version intermédiaire.
- `size` entre 1 000 000 et 24 000 000 octets (les bornes de `qp-update`).
- La signature porte sur `openqiara-claude-update-v1\n` suivi du fichier `update.json` **tel quel, octet pour octet** :
  ne jamais le rééditer à la main après la signature. La caméra le lit strictement : un seul objet JSON en UTF-8,
  les seules clés ci-dessus (casse exacte), chacune une fois, rien après. `version` : 1 à 64 caractères imprimables.

## La clé de signature

La clé **privée** est créée et gardée par le propriétaire du dépôt, sur son poste. Elle n'est jamais dans le dépôt, jamais
sur la caméra, jamais dans une conversation avec Claude, jamais en argument de ligne de commande.

```sh
go build -o oqsign ./cmd/oqsign                 # ou le binaire d'oqsign livré à part
./oqsign keygen -out ~/cles/openqiara-maj.oqkey # crée le fichier en 0600, sans jamais écraser un fichier existant
```

`keygen` n'affiche que la clé publique et son empreinte. À faire ensuite :

1. **Sauvegarder** le fichier de clé privée hors ligne (clé USB chiffrée, gestionnaire de mots de passe). Perdue, plus
   aucune version signée ne peut être publiée : il faudrait installer à la main une version portant une nouvelle clé.
2. Graver la clé **publique** : l'ajouter à `ProductionKeys` dans `internal/sigupdate/keys.go`, puis reconstruire.
   Avant de livrer une version : `SIGUPDATE_REQUIRE_PROD_KEY=1 go test ./internal/sigupdate` (échoue tant que la liste est vide).
3. **Rotation** : graver l'ancienne et la nouvelle clé ensemble dans une version, signer cette version avec l'ancienne,
   puis les suivantes avec la nouvelle, et retirer l'ancienne à la version d'après.
4. **Clé compromise** : le dépôt seul ne suffit pas à nuire (il faut la clé), mais celui qui a la clé peut signer.
   Publier à la main (outil de mise à niveau) une version qui ne grave plus la clé compromise ; les caméras qui ne l'ont pas
   reçue restent exposées jusque-là.

`./oqsign pubkey -key <fichier>` réaffiche la clé publique ; `oqsign` refuse une clé privée lisible par d'autres
(Linux, macOS). Sous Windows, qui n'a pas de droits Unix, `oqsign.exe` ne peut pas le contrôler : placer le fichier dans
un dossier réservé à son propriétaire (propriétés > Sécurité) et ne pas le synchroniser avec un nuage.

## Publier une version signée

```sh
# 1. construire en donnant le seq (4e argument) : il est compilé dans le binaire (OQSEQ=17;)
sh scripts/build-arm.sh v17 2026-11-20 openqiarad-claude-v17 17
# 2. signer (le seq doit être celui du binaire ; oqsign refuse un fichier qui n'est pas un ELF ARM 32 bits)
./oqsign sign -key ~/cles/openqiara-maj.oqkey -in openqiarad-claude-v17 -version v0.1.0-rc.23-claude-v17 -seq 17 -outdir release/
# 3. contrôler ce qui sera publié
./oqsign verify -pub <clé publique> -manifest release/update.json -bin openqiarad-claude-v17
```

4. Créer la release GitHub **en brouillon**, y joindre les trois fichiers, puis la publier : une release n'est « latest »
   qu'une fois publiée, et la caméra ne doit jamais voir un manifeste sans son binaire. Une pré-version n'est pas « latest » :
   elle n'est pas proposée.
5. Seule la **dernière** release compte : les manifestes d'un autre canal doivent être joints à la dernière release aussi.

Retirer une version défectueuse : **publier un `seq` supérieur** avec le binaire corrigé (ou la version précédente
recompilée sous un nouveau `seq`). Supprimer une release n'efface rien sur les caméras qui l'ont déjà vue : un manifeste
plus ancien que le plus récent déjà vu est refusé (anti-retour-arrière).

## Première installation et activation

La V16 ne sait pas se mettre à jour : **la V17 elle-même s'installe une fois à la main** (outil de mise à niveau
de la distribution, caméra désarmée), comme les versions précédentes. Ensuite seulement :

```sh
# réglages (authentification administrateur de l'interface) ; 409 tant qu'aucune clé n'est gravée ou que QIARA Plus manque
curl -u admin -X PUT http://<caméra>/api/v1/update/signed -H 'Content-Type: application/json' \
     -d '{"enabled":true,"repo":"<propriétaire>/<dépôt>","interval_hours":12}'
curl -u admin http://<caméra>/api/v1/update/signed        # état : dernier résultat, version vue, version en service
```

Couper : `{"enabled":false}` (toujours possible). Pris en compte à la vérification suivante, sans redémarrage.

## Retour arrière

- **Automatique** : si la version signée ne passe pas l'essai, QIARA Plus revient à `good`. Elle n'est pas réessayée.
- **Par Home Assistant** : les boutons de QIARA Plus (`update-rollback` pendant l'essai, `use-prev` après).
- **Couper la mise à jour signée** : `{"enabled":false}`.
- **Retirer la fonction** : réinstaller à la main une version antérieure ; l'option `signed_update` du fichier de
  configuration est ignorée par elle.

## Ce que les caméras voient (Home Assistant et MQTT)

Quatre entités de diagnostic, **seulement une fois une clé gravée** : « Mise à jour signée » (dernier résultat : Désactivée,
À jour, En attente, Installation en essai, Installée, Retour arrière, Signature refusée, Version refusée, Dépôt injoignable,
Erreur), « Version signée en service » (`seq`), « Dernière version signée vue », « Dernière vérification ». Le détail
(`status.message`, `pending_seq`, `failed_seq`) est dans `GET /api/v1/update/signed`. La mémoire est dans
`/data/qp/sigupdate.json` (une écriture par vérification, écrite puis synchronisée sur la carte). L'effacer fait
oublier le plus grand `seq` vu, les versions revenues en arrière ou quittées : à ne faire que pour réautoriser
sciemment une version écartée. Le paquet `home-assistant/packages/openqiara_claude_v17_mise_a_jour_signee.yaml`
notifie l'essai, l'installation, le retour arrière et les refus.

## Ce que ça ne couvre pas

- Pas d'expiration : un serveur qui sert l'ancien manifeste *le plus récent* (« gel ») empêche seulement les mises à jour ;
  il ne peut pas faire revenir en arrière (`seq` inférieur au plus récent vu refusé).
- Pas de vérification d'un flux vidéo en cours avant la relance : seules l'alarme, l'essai en cours et l'état de
  QIARA Plus sont contrôlés. La relance coupe le flux quelques secondes.
- Un seul propriétaire de clé : celui qui la détient peut signer.
- Il faut un accès HTTPS à `github.com` depuis la caméra (c'est déjà celui de l'OTA de l'amont), et une horloge
  réglée (NTP) : avant, les certificats sont refusés et la vérification échoue (`echec-reseau`).
- Construire la version de livraison avec la **dernière version corrective de Go 1.26** (1.26.11 au 21/11/2026) :
  la 1.26.1 a des failles connues dans `crypto/x509`, `crypto/tls` et `net/http` atteignables par ce client HTTPS
  (rapport `govulncheck`).
- Aucune installation de la V17 sur une vraie caméra n'a été faite par ce chantier : voir la note d'état.

## Dépannage

| Résultat | Sens |
|---|---|
| `pas-de-cle` | aucune clé gravée : fonction inerte |
| `desactivee` | `enabled` faux |
| `echec-reseau` | dépôt injoignable, fichier absent (release sans `update.json`), réponse trop grosse, somme ou taille du binaire différentes de celles signées |
| `echec-signature` | signature absente, fausse, ou clé non gravée |
| `refusee` | canal différent, `seq` plus ancien que le plus récent vu, `min_seq` non atteint, version déjà revenue en arrière |
| `attente` | alarme armée ou état inconnu, essai de QIARA Plus en cours, QIARA Plus absent ou incomplet, place insuffisante ou illisible, relance non faite par QIARA Plus (essai annulé) |
| `installation` | version déposée chez QIARA Plus ; essai en cours |
| `installee` / `retour-arriere` | issue de l'essai, constatée au démarrage suivant ou à la vérification suivante ; `retour-arriere` aussi quand on est revenu à une version antérieure (bouton, réinstallation) |

Journal : lignes « mise à jour signée » du journal d'openqiarad ; côté QIARA Plus : `qp-update status`.
