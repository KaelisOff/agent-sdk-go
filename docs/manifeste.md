# Le manifeste (`manifest.toml`)

Le manifeste décrit l'agent et ce qu'il demande à la box. Il est lu à l'installation : le propriétaire
voit exactement ce qui y est demandé. Les **clés inconnues sont refusées**, pour qu'un manifeste écrit
pour une box plus récente (avec une capacité que celle-ci ne connaît pas) ne soit jamais installé comme
s'il demandait moins.

`kagent -check manifest.toml` le vérifie avec les mêmes règles que la box (paquet
[`manifest`](../manifest)).

## Identité

| Clé | Obligatoire | Règle |
|---|---|---|
| `id` | oui | identifiant inversé, en minuscules, 3 à 7 parties : `io.github.votre-compte.mon-agent`, ou `fr.mondomaine.mon-agent` si vous avez un domaine. Il doit désigner un compte ou un domaine **que vous contrôlez** : le magasin le vérifiera. |
| `name` | oui | 1 à 60 caractères, affiché dans le Centre d'agents |
| `version` | oui | `MAJEUR.MINEUR.CORRECTIF`, ex. `1.4.0` |
| `description` | non | 2 000 caractères max |
| `author` | non | vous |
| `license` | oui | identifiant SPDX d'une licence libre (`Apache-2.0`, `MIT`, `GPL-3.0-or-later`…) : les agents sont open source |
| `source` | oui | le dépôt public dont le paquet est construit |

## `[run]` : les ressources d'un passage

| Clé | Par défaut | Maximum |
|---|---|---|
| `memory_mb` | 128 | 512 (minimum 16), sans swap |
| `cpus` | 1 | 2 (minimum 0,1) |
| `pids` | 64 | 128 (minimum 4) |
| `timeout_min` | 10 | 30 |

Au-delà de `memory_mb`, l'agent est tué ; au-delà de `timeout_min`, le passage est arrêté.

## `[permissions]` : les capacités demandées

| Clé | Capacité |
|---|---|
| `model = true` | interroger le modèle de la box |
| `storage_kb = N` | une mémoire privée (clé → valeur) de N Ko, 16 384 max |
| `schedule = ["…"]` | se lancer seul à ces horaires, 8 max |
| `notify = true` | envoyer des notifications au propriétaire et aux administrateurs |
| `approval = true` | leur demander un accord |
| `mail_send = N` | envoyer jusqu'à N emails par jour (200 max), avec les garde-fous de la box |
| `mail_read = true` | lire les réponses à ses propres emails |

Ne demandez que ce qui sert : chaque capacité est montrée au propriétaire, qui doit toutes les accepter.
Une mise à jour qui en demande une nouvelle est signalée.

### Horaires

Syntaxe cron classique à 5 champs, dans le fuseau horaire de la box :
`minute heure jour-du-mois mois jour-de-la-semaine`.

- `*`, nombres, intervalles (`1-5`), listes (`1,15`), pas (`*/15`, `9-17/2`) ;
- jour de la semaine : 0 ou 7 = dimanche, 1 = lundi ;
- si le jour du mois **et** le jour de la semaine sont précisés, l'un ou l'autre suffit.

| Expression | Signifie |
|---|---|
| `0 9 * * 1-5` | 9 h, du lundi au vendredi |
| `30 8 * * 1` | le lundi à 8 h 30 |
| `0 18 1 * *` | le 1er du mois à 18 h |
| `*/30 9-18 * * 1-5` | toutes les demi-heures en journée, en semaine |

Un horaire manqué pendant que la box était éteinte n'est pas rattrapé.

## Exemple complet

```toml
id = "io.github.votre-compte.relances"
name = "Relances de devis"
version = "1.0.0"
description = "Chaque lundi, liste les devis sans réponse et propose une relance."
author = "Votre nom"
license = "Apache-2.0"
source = "https://github.com/votre-compte/relances"

[run]
memory_mb = 64
timeout_min = 10

[permissions]
model = true
notify = true
approval = true
storage_kb = 256
schedule = ["30 8 * * 1"]
```
