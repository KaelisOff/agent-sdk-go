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
- binaire Go compilé avec `-trimpath` (pas de chemins locaux), `-buildvcs=false` (pas d'état du dépôt
  git, qui changerait d'une copie à l'autre) et `-ldflags '-s -w'`.

La seule autre chose qui compte est la **version de Go** : notez-la (ligne `toolchain` du `go.mod`,
ou dans votre CI).

Pour vérifier : construisez deux fois et comparez (`cmp a.kagent b.kagent`), ou comparez l'empreinte
`digest` affichée par `kagent`.

## Le magasin : recompiler un paquet publié

Le magasin d'agents ne prend pas de paquets envoyés à la main : il **recompile chaque version depuis le
dépôt public** (`source` du manifeste), à l'étiquette soumise, fait ses analyses automatiques et une
revue, puis publie le paquet dans un catalogue signé. Chaque version publiée donne sa **recette** :
dépôt, commit, version de Go, version de `kagent`, empreinte du paquet. Pour refaire la compilation
du magasin et comparer :

```sh
git clone --depth 1 --branch v1.2.0 https://github.com/compte/mon-agent src && cd src
git rev-parse HEAD                     # doit être le commit de la recette
export GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=readonly   # la version de Go de la recette
GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 go build -trimpath -buildvcs=false -ldflags '-s -w' -o agent .
kagent -manifest manifest.toml -executable agent -o agent.kagent   # la version de kagent de la recette
sha256sum agent.kagent                 # doit être l'empreinte publiée
```

Le magasin compile **sans réseau** (les modules sont téléchargés avant, par le proxy Go et vérifiés
par la base d'empreintes), avec le cache de modules plutôt qu'un dossier `vendor/` (dont le contenu
n'est vérifié par personne), et compile deux fois pour s'assurer que le résultat est reproductible.

La signature suit le format du module public
[`github.com/KaelisOff/trust`](https://github.com/KaelisOff/trust) : un catalogue signé (`index.json`)
donne la taille et l'empreinte SHA-256 de chaque paquet publié, et les boîtes refusent tout paquet qui
ne correspond pas. Chacun peut vérifier le catalogue et les paquets avec ce module.
