# serveur-http

Un serveur HTTP/1.1 écrit en Go **sans `net/http`**, directement sur une socket TCP. Le but : comprendre ce que les librairies cachent, c'est-à-dire le parsing des octets, la concurrence, le keep-alive, les requêtes Range, gzip et chunked.

Son usage : partager un dossier **en lecture seule** sur le Wi-Fi de la maison, pour lire des PDF de cours ou regarder des vidéos depuis un téléphone, en avançant dedans.

Bibliothèque standard uniquement, sans aucune dépendance externe.

## Lancer

Il faut Go 1.27 ou plus récent.

```sh
go build -o serveur-http.exe ./cmd/serveur-http

# Sur ce PC seulement (127.0.0.1)
./serveur-http.exe -dir ./mon-dossier

# Ouvert au Wi-Fi, avec un mot de passe tiré au hasard
./serveur-http.exe -lan -dir ./mon-dossier
```

Avec `-lan`, le serveur affiche les adresses à taper sur le téléphone (ex. `http://192.168.1.16:8080`) et le mot de passe du jour. Le nom d'utilisateur peut être n'importe lequel. Sous Windows, le profil du réseau doit être « Privé », et le pare-feu ne doit autoriser que les réseaux privés.

| Option | Défaut | Rôle |
|---|---|---|
| `-dir` | `.` | dossier partagé |
| `-port` | `8080` | port d'écoute |
| `-lan` | non | écoute sur toutes les cartes réseau, mot de passe obligatoire |
| `-max-conns` | `100` | connexions simultanées au maximum |

Ctrl+C arrête le serveur proprement : il n'accepte plus de connexions et laisse 5 s aux téléchargements en cours.

## Architecture

```
  client (navigateur, curl, téléphone)
     │  octets TCP
     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ internal/server   boucle Accept                                         │
│                   ├─ place libre ? (sémaphore global + limite par IP)   │
│                   │     non → 503 immédiate, puis fermeture             │
│                   └─ oui → une goroutine par connexion                  │
│                        boucle keep-alive : délai d'inactivité, 100 req. │
│                        délais de lecture, d'écriture et débit minimum   │
└──────────┬──────────────────────────────────────────────────────────────┘
           ▼
┌──────────────────────────┐   ┌───────────────────────────────────────────┐
│ internal/http1           │   │ internal/auth (seulement avec -lan)       │
│ ParseRequest :           │──▶│ Basic Auth, comparaison en temps constant │
│ ligne + en-têtes (CRLF), │   │ 10 échecs/min par IP → 429 pendant 1 min  │
│ limites 414/431, refus   │   └─────────────────┬─────────────────────────┘
│ 400/501/505, smuggling   │                     ▼
└──────────────────────────┘   ┌───────────────────────────────────────────┐
                               │ internal/files                            │
                               │ chemin nettoyé → os.Root (anti-traversée) │
                               │ dossier → listing HTML (html/template+CSP)│
                               │ fichier → Range 206/416, 304, type MIME   │
                               └─────────────────┬─────────────────────────┘
                                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ internal/http1   WriteResponse                                          │
│   fichier binaire ou plage → Content-Length, copie en continu           │
│   texte + Accept-Encoding: gzip → gzip au fil de l'eau + chunked        │
└─────────────────────────────────────────────────────────────────────────┘
```

| Paquet | Rôle |
|---|---|
| `cmd/serveur-http` | les options, la socket, l'arrêt sur Ctrl+C |
| `internal/server` | les connexions : concurrence, limites, délais, keep-alive, journal |
| `internal/http1` | le protocole : lire une requête, écrire une réponse (Range, gzip, chunked) |
| `internal/files` | le dossier partagé : chemins, fichiers, listing |
| `internal/auth` | le mot de passe et le blocage des essais |
| `cmd/bench` | le client de charge |

Le serveur ne connaît pas les fichiers : il appelle un `Handler`, une fonction qui reçoit une requête et rend une réponse. Le mot de passe est un handler qui en enveloppe un autre.

## Choix techniques

- **Parser strict** (RFC 9112) : `\r\n` obligatoire, cible en ASCII visible seulement, `Host` unique. Un parser tolérant ouvre la porte au *request smuggling*. Pour la même raison, `Content-Length` avec `Transfer-Encoding` reçoit une 400.
- **Une goroutine par connexion**, avec un sémaphore (un channel bufferisé) pour en limiter le nombre. Quand tout est plein, le client reçoit une 503 tout de suite au lieu d'attendre : un client lent ne bloque pas la file des autres.
- **Délais** : la lecture de la requête a un délai global (contre *slowloris*), l'écriture un délai relancé à chaque morceau, plus un débit minimum de 1 Kio/s (contre le *slow read*). Un long téléchargement qui avance n'est jamais coupé.
- **Fermeture en douceur** (*lingering close*) : avant de fermer après une erreur, le serveur lit et jette ce que le client envoie encore. Sinon, le système enverrait un *reset* TCP qui ferait perdre la réponse.
- **Fichiers en continu** : un fichier n'est jamais chargé en entier. Un fichier de 2 Go est servi avec 10 Mo de mémoire.
- **Range** : c'est ce qui permet d'avancer dans une vidéo. Une plage reçoit une 206, une plage impossible une 416, et `If-Range` est géré.
- **gzip au fil de l'eau** : la taille compressée n'est pas connue à l'avance, donc la réponse part en `Transfer-Encoding: chunked`, écrit à la main. Seuls le texte et le listing sont compressés. Un cours de 1,18 Mo arrive en 53 Ko.
- **Anti-traversée de chemin en 3 barrières** : `path.Clean`, `filepath.IsLocal`, puis `os.Root`, qui refuse au niveau du système toute sortie du dossier, y compris par un lien symbolique.
- **Mot de passe** tiré avec `crypto/rand` à chaque lancement, et comparé en temps constant (`crypto/subtle` sur des empreintes SHA-256), pour qu'on ne puisse pas le deviner en mesurant les temps de réponse.

## Benchmark

`go run ./cmd/bench` lance le serveur dans le même programme, puis envoie 5000 requêtes GET d'un fichier de 1 Kio avec 50 clients en parallèle, d'abord avec keep-alive, puis avec une nouvelle connexion TCP par requête. Le client de charge utilise `net/http`, ce qui est permis côté client.

Mesures du 28/11/2026 (Windows 11, 12 cœurs, client et serveur sur la même machine, journal coupé). Ce sont les médianes de 3 passages :

| Mode | 50 clients | 1 client |
|---|---|---|
| keep-alive | **11 400 req/s** | 4 000 req/s |
| une connexion par requête | 4 200 req/s | 2 000 req/s |

Le keep-alive multiplie le débit par 2 à 2,7. Il évite pour chaque requête la poignée de main TCP, une nouvelle goroutine et la fermeture de la connexion.

## Sécurité

- Le serveur est en lecture seule : seules les méthodes GET et HEAD sont acceptées, les autres reçoivent une 405.
- Par défaut, il n'écoute que sur `127.0.0.1`. L'ouverture au réseau se fait seulement avec `-lan`, et elle impose un mot de passe.
- Le listing échappe les noms de fichiers (`html/template`) et envoie une CSP par empreinte. Les `.html` et `.svg` servis reçoivent une CSP `sandbox`.
- Chaque version passe Semgrep (`p/golang`, `p/secrets`) et Gitleaks.

## Limites

- **Pas de HTTPS** : avec `-lan`, le mot de passe circule en clair sur le Wi-Fi. À utiliser chez soi, pas sur un Wi-Fi public.
- HTTP/1.1 seulement, sans HTTP/2.
- Lecture seule, sans envoi de fichiers depuis le téléphone.

## Tests

```sh
go test -race ./...   # -race demande cgo (gcc) sur Windows
```

Plus de 130 tests : des tests en table pour le parser, et des tests d'intégration sur une vraie socket (avec `net.Dial` octet par octet, et avec le client `net/http`). Ils couvrent la traversée de chemin, slowloris, le slow read, 200 clients en parallèle, le pipelining, un fichier de 2 Go, gzip et chunked, et la limite par IP. Chaque test important a été vérifié par mutation : on casse le code exprès, et le test doit échouer.

## Historique
- **V0.0** : le parser de requêtes (RFC 9112), avec les limites de taille et des refus stricts (400 / 414 / 431 / 501 / 505)
- **V0.1** : serveur TCP qui répond « Hello », réponses d'erreur, `recover` des handlers, fermeture en douceur
- **V0.2** : servir un dossier en lecture seule (fichiers en continu, listing HTML, HEAD, 301/403/404/405), anti-traversée en 3 barrières
- **V0.3** : une goroutine par connexion, délais (408), limite de connexions, arrêt propre sur Ctrl+C, journal
- **V0.4** : keep-alive avec pipelining, délai d'inactivité, refus du request smuggling
- **V0.5** : requêtes Range (206, 416, If-Range), Last-Modified et 304
- **V0.6** : gzip au fil de l'eau et `Transfer-Encoding: chunked` écrit à la main
- **V0.7** : accès Wi-Fi (`-lan`) avec mot de passe, blocage en 429, limite par IP, 503 immédiate, débit minimum. Testé sur un vrai téléphone
- **V1.0** : README, benchmark (`cmd/bench`)
