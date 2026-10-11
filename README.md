# Kit de développement des agents Kaelis (Go)

Kaelis est une box d'IA qui tourne **chez vous** : le modèle, les conversations et les documents
restent dans la boîte, rien ne part dans le cloud.

Un **agent** est une automatisation qu'on installe sur la box, comme une application : il travaille
seul (à heure fixe, à la demande, ou quand on répond à l'une de ses questions) avec le modèle d'IA de
la box. Les agents sont **open source** et n'importe qui peut en écrire un.

Ce dépôt contient tout ce qu'il faut pour en écrire un en Go :

| Paquet | Rôle |
|---|---|
| [`agent`](agent) | le lien entre votre agent et la box (protocole, capacités) |
| [`agenttest`](agenttest) | une box simulée pour tester votre agent avec `go test`, sans box |
| [`manifest`](manifest) | lecture et vérification du manifeste, avec les règles de la box |
| [`pack`](pack), [`cmd/kagent`](cmd/kagent) | construction du paquet `.kagent`, reproductible |
| [`examples/hello`](examples/hello) | un agent complet : un conseil chaque matin, une question tous les trois conseils |

Le plus simple pour commencer : le modèle **[agent-template-go](https://github.com/KaelisOff/agent-template-go)**
(bouton *Use this template*).

## Le principe : des capacités, jamais d'accès direct

Un agent n'a **ni réseau, ni accès aux fichiers, ni secret**. Il tourne dans un conteneur fermé et ne
parle à la box que par son entrée et sa sortie standard. Pour agir, il demande à la box d'utiliser une
**capacité** ; c'est la box qui exécute et qui applique les garde-fous. Votre code ne peut pas les
contourner, et vous n'avez pas à les réécrire.

| Capacité | Dans le manifeste | Dans le code | Limites |
|---|---|---|---|
| modèle | `model = true` | `box.Chat`, `box.ChatMax` | 24 000 caractères et 2 048 tokens par appel |
| mémoire | `storage_kb = 64` | `box.Get`, `box.Set`, `box.GetJSON`, `box.SetJSON`, `box.Delete`, `box.Keys` | la taille demandée (16 Mo max) |
| horaires | `schedule = ["0 9 * * 1-5"]` | `run.Trigger.Kind == "schedule"` | 8 horaires max |
| notification | `notify = true` | `box.Notify` | titre 160 caractères, texte 4 000 |
| demande d'accord | `approval = true` | `box.RequestApproval` | 10 en attente max ; sans réponse avant l'échéance, c'est non |

Le propriétaire de la box voit la liste des capacités avant l'installation et doit toutes les accepter.
D'autres capacités arrivent (envoi et lecture d'emails avec garde-fous, recherche web) ; elles seront
ajoutées ici au fur et à mesure.

## Un agent

```go
package main

import "github.com/KaelisOff/agent-sdk-go/agent"

func main() {
	agent.Main(func(box *agent.Box, run agent.Run) (string, error) {
		conseil, err := box.Chat(agent.User("Donne un conseil court à un indépendant."))
		if err != nil {
			return "", err
		}
		return "conseil envoyé", box.Notify("Conseil du jour", conseil)
	})
}
```

Et son manifeste, `manifest.toml` ([toutes les clés](docs/manifeste.md)) :

```toml
id = "io.github.votre-compte.conseil"
name = "Conseil du jour"
version = "0.1.0"
description = "Un conseil court chaque matin de semaine."
author = "Votre nom"
license = "Apache-2.0"
source = "https://github.com/votre-compte/conseil"

[run]
memory_mb = 64
timeout_min = 5

[permissions]
model = true
notify = true
schedule = ["0 9 * * 1-5"]
```

## Tester sans box

`agenttest` joue le rôle de la box : mêmes vérifications, mêmes limites, mêmes codes d'erreur. Vous
choisissez ce que répond le modèle et vous vérifiez ce que l'agent a fait.

```go
func TestConseil(t *testing.T) {
	box, err := agenttest.Load("manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	box.Model = func([]agent.Message, int) (string, error) {
		return "Relancez vos devis du mois dernier.", nil
	}
	res := box.Run(run, agenttest.Scheduled("0 9 * * 1-5"))
	if res.Status != "ok" || len(box.Notifications) != 1 {
		t.Fatalf("%+v\n%s", res, res.Logs)
	}
}
```

Les demandes d'accord se testent avec `box.Answer(id, true)` ou `box.Expire(id)`, qui renvoient le
déclencheur du passage suivant. Voir [`examples/hello/main_test.go`](examples/hello/main_test.go).

## Construire le paquet

```sh
go install github.com/KaelisOff/agent-sdk-go/cmd/kagent@latest
kagent -check manifest.toml
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o agent .
kagent -manifest manifest.toml -executable agent
```

Le paquet est **reproductible** : les mêmes sources, compilées avec la même version de Go, donnent
exactement les mêmes octets. C'est ce qui permettra au magasin d'agents de recompiler chaque agent
publié depuis son dépôt public et de vérifier que le paquet distribué est bien celui des sources.
Détails : [docs/paquet.md](docs/paquet.md).

Pour l'essayer, le propriétaire ou un administrateur de la box l'installe depuis le **Centre
d'agents** (*Installer un agent*), après avoir lu et accepté ses permissions.

## À savoir pour écrire un agent

- Le binaire doit être **statique** (`CGO_ENABLED=0`) : l'image ne contient que lui.
- Rien n'est conservé entre deux passages, sauf ce que vous mettez dans la **mémoire** (`box.Set`).
  `/tmp` existe (16 Mo, non exécutable) mais est vidé à chaque passage.
- Pas de réseau, pas de DNS, pas de variables d'environnement : tout passe par les capacités.
- Écrivez vos journaux avec `box.Logf` (sortie d'erreur) ; **la sortie standard est réservée au
  protocole**. Une ligne qui n'est pas un message JSON arrête l'agent.
- `run.Now` est déjà dans le fuseau de la box. L'image n'a pas de base de fuseaux horaires : pour en
  utiliser d'autres, importez `time/tzdata`.
- Un passage s'arrête après sa durée maximale (`timeout_min`) ou 200 appels : découpez les gros
  travaux sur plusieurs passages, avec la mémoire.

Référence complète : [manifeste](docs/manifeste.md) · [protocole](docs/protocole.md) ·
[format du paquet](docs/paquet.md).

## État

Version 0.x : le protocole (version 1) est stable, l'API Go peut encore bouger avant la 1.0. Les
retours sont bienvenus dans les *issues*.

Une faille de sécurité ? Ne l'ouvrez pas en *issue* publique : voir
[SECURITY.md](https://github.com/KaelisOff/.github/blob/main/SECURITY.md).

## Licence

[Apache 2.0](LICENSE).
