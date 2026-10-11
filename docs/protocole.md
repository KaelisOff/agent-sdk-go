# Le protocole (version 1)

Ce que fait le paquet [`agent`](../agent) pour vous. À lire si vous écrivez un agent dans un autre
langage que Go, ou par curiosité.

## Le passage d'un agent

À chaque passage (horaire, lancement à la main, réponse à une demande d'accord), la box démarre le
conteneur de l'agent, puis :

1. elle écrit un message `start` sur son **entrée standard** ;
2. l'agent envoie des **appels** sur sa **sortie standard**, la box répond à chacun, dans l'ordre ;
3. l'agent peut envoyer un message `done` avec un résumé, puis se termine.

Un message = un objet JSON sur une ligne (terminée par `\n`), 1 Mo maximum. La **sortie d'erreur** est
un journal libre, que le propriétaire peut lire (16 Ko gardés par passage).

Le passage s'arrête aussi, et l'agent est tué, si :

- il dépasse sa durée (`timeout_min` du manifeste) ou sa mémoire ;
- il écrit sur la sortie standard une ligne qui n'est pas un message JSON, ou de plus d'1 Mo ;
- il fait plus de 200 appels.

## Messages

```
box → agent   {"type":"start","protocol":1,"run":{…}}
agent → box   {"type":"call","id":1,"method":"model.chat","params":{…}}
box → agent   {"type":"result","id":1,"result":{…}}
          ou  {"type":"error","id":1,"error":{"code":"…","message":"…"}}
agent → box   {"type":"done","summary":"…"}
```

`id` est un entier choisi par l'agent, repris dans la réponse. Le résumé (500 caractères gardés)
s'affiche dans l'historique de l'agent.

### `start`

```json
{
  "type": "start",
  "protocol": 1,
  "run": {
    "id": "run_…",
    "agent_id": "io.github.votre-compte.mon-agent",
    "version": "1.0.0",
    "trigger": {"kind": "schedule", "schedule": "0 9 * * 1-5"},
    "now": "2026-10-12T09:00:00+02:00",
    "time_zone": "Europe/Paris",
    "locale": "fr-FR"
  }
}
```

`trigger.kind` vaut :

- `schedule` : un des horaires du manifeste (`trigger.schedule`) ;
- `manual` : lancé depuis le Centre d'agents ;
- `approval` : une demande d'accord a reçu une réponse ou a expiré :
  `"approval": {"id": "…", "approved": true, "expired": false, "data": …}` (`data` est celle de la
  demande).

## Méthodes

| Méthode | Capacité | Paramètres | Résultat |
|---|---|---|---|
| `model.chat` | `model` | `messages` : 1 à 50 `{"role","content"}` (`system`, `user`, `assistant`), 24 000 caractères au total ; `max_tokens` : 1 à 2 048 (512 par défaut) | `{"text","prompt_tokens","output_tokens"}` |
| `storage.get` | `storage` | `key` (1 à 200 octets) | `{"value": "…"}` ou `{"value": null}` |
| `storage.set` | `storage` | `key`, `value` | `{}` ; `quota_exceeded` au-delà de `storage_kb` (clés + valeurs) |
| `storage.delete` | `storage` | `key` | `{}` |
| `storage.list` | `storage` | `prefix` | `{"keys": […]}`, triées, 1 000 max |
| `notify` | `notify` | `title` (1 à 160 caractères), `body` (4 000 max) | `{"id"}` |
| `approval.request` | `approval` | `title` (1 à 160), `details` (4 000 max), `data` (JSON libre, 16 Ko max), `expires_hours` (1 à 168, 72 par défaut) | `{"id"}` ; `quota_exceeded` si 10 demandes attendent déjà |
| `mail.send` | `mail.send` | `to` (une adresse seule), `subject` (1 à 150, une ligne), `body` (texte, 5 000 max), `campaign`, `variant` (facultatifs, 40 caractères a-z 0-9 - _) | `{"id", "remaining"}` ; refus ci-dessous |
| `mail.replies` | `mail.read` | `after` (curseur, 0 au début), `limit` (100 max) | `{"replies": [{"id", "kind", "to", "from", "subject", "text", "campaign", "variant", "sent_at", "received_at"}], "next"}` |
| `mail.status` | `mail.send` ou `mail.read` | aucun | `{"configured", "paused", "window_open", "next_window", "sent_today", "limit_today", "agent_sent_today", "agent_limit", "stats": [{"campaign", "variant", "sent", "replied", "auto_replies", "unsubscribed", "bounced"}]}` |

Les paramètres inconnus sont refusés (`invalid_params`). Le modèle est partagé avec les conversations
de la box : un appel peut attendre qu'une génération en cours se termine.

## Codes d'erreur

| Code | Signifie |
|---|---|
| `permission_denied` | la capacité n'est pas dans le manifeste (ou n'a pas été accordée) |
| `invalid_params` | paramètres absents, en trop ou hors limites |
| `quota_exceeded` | mémoire pleine, trop de demandes d'accord en attente |
| `unknown_method` | méthode inconnue de cette box |
| `too_many_calls` | plus de 200 appels dans ce passage (l'agent est ensuite arrêté) |
| `failed` | la box n'a pas pu faire l'action (modèle indisponible, serveur mail injoignable…) |

Refus propres aux emails (`mail.send`) :

| Code | Signifie |
|---|---|
| `not_configured` | aucune messagerie n'est configurée sur la box |
| `already_contacted` | cette adresse a déjà reçu un email (un seul par adresse, pour toujours) |
| `opted_out` | la personne a répondu STOP, ou l'adresse n'existe pas |
| `outside_hours` | hors de la plage d'envoi (lundi au samedi, 8 h à 19 h) |
| `quota_exceeded` | limite du jour atteinte (celle de la box ou celle du manifeste) |
| `paused` | envois en pause (trop d'erreurs ou de désinscriptions) : le propriétaire doit reprendre |
| `bounced` | le serveur a refusé cette adresse ; elle est bloquée |

`kind` d'une réponse : `reply`, `auto` (réponse automatique, absence), `unsubscribe` (la personne
s'est désinscrite : l'adresse est déjà bloquée), `bounce` (l'adresse n'existe pas). Le texte d'une
réponse est ce que la personne a écrit, sans le message cité.

## Ce que la box enregistre

Chaque passage, et chaque appel qui agit hors de l'agent (modèle, notification, demande d'accord), est
inscrit au journal d'audit de la box, **jamais leur contenu**.
