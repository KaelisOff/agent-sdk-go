# Le paquet `.kagent`

Un paquet est une archive tar qui contient, dans cet ordre :

```
manifest.toml   le manifeste, tel qu'écrit
image.tar       l'image du conteneur, au format OCI
```

L'image contient un seul fichier exécutable, `/agent` (statique), plus les dossiers vides `/etc` et
`/tmp`. Elle démarre `/agent` en tant que `nobody` (65534). La box la nomme
`localhost/kaelis-agent/<id>:<version>` et ne lance que des images chargées depuis un paquet ; elle ne
télécharge jamais rien.

`kagent` (paquet [`pack`](../pack)) construit le paquet sans Docker ni Podman.

## Reproductible

Les mêmes sources donnent toujours les mêmes octets :

- dates de tous les fichiers fixées au 1er janvier 1970, propriétaires et droits fixes ;
- ordre des fichiers fixe, archives tar au format PAX ;
- compression gzip au niveau maximal, sans nom ni date ;
- binaire Go compilé avec `-trimpath` (pas de chemins locaux) et `-ldflags '-s -w'`.

La seule autre chose qui compte est la **version de Go** : notez-la (ligne `toolchain` du `go.mod`,
ou dans votre CI).

Pour vérifier : construisez deux fois et comparez (`cmp a.kagent b.kagent`), ou comparez l'empreinte
`digest` affichée par `kagent`.

## Ce qui viendra avec le magasin

Le magasin d'agents ne prendra pas de paquets envoyés à la main : il **recompilera chaque version depuis
le dépôt public** (`source` du manifeste), vérifiera qu'il retrouve les mêmes octets que l'auteur, fera
ses analyses automatiques (et une revue humaine pour une première publication ou une nouvelle
permission), puis signera le paquet.
