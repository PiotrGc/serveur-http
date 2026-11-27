# serveur-http

Un serveur HTTP/1.1 écrit en Go **sans `net/http`**, directement sur une socket TCP, pour comprendre ce que les librairies cachent : parsing des octets, concurrence, keep-alive, requêtes Range, gzip et chunked.

Son usage : partager un dossier en lecture seule sur le Wi-Fi pour lire ses PDF ou ses vidéos depuis un téléphone.

## État
- [x] **V0.0** : le parser de requêtes (RFC 9112), avec les limites de taille et des refus stricts (400 / 414 / 431 / 501 / 505)
- [x] **V0.1** : serveur TCP qui répond « Hello », réponses d'erreur, `recover` des handlers, fermeture en douceur (lingering close)
- [x] **V0.2** : servir un dossier en lecture seule (fichiers lus en continu, listing HTML, HEAD, 301/403/404/405). Anti-traversée en 3 barrières (`cleanPath`, `filepath.IsLocal`, `os.Root`), listing échappé par `html/template` avec une CSP par empreinte
- [x] **V0.3** : une goroutine par connexion, délai de lecture global (anti-slowloris, 408), délai d'écriture relancé à chaque morceau, limite de connexions (sémaphore), arrêt propre sur Ctrl+C, une ligne de journal par requête
- [x] **V0.4** : keep-alive (RFC 9112 §9.3) avec pipelining, délai d'inactivité, 100 requêtes par connexion au maximum, corps de requête sauté, refus du request smuggling (Content-Length + Transfer-Encoding)
- [x] **V0.5** : requêtes Range (206, 416, If-Range), Last-Modified et 304, Accept-Ranges. Un fichier de 2 Go est servi avec 10 Mo de mémoire
- [x] **V0.6** : compression gzip au fil de l'eau (texte et listing seulement, `Accept-Encoding` avec poids q, `Vary`) et `Transfer-Encoding: chunked` écrit à la main. Un cours de 1,18 Mo arrive en 53 Ko
- [ ] V0.7 : accès Wi-Fi avec mot de passe
- [ ] V1.0 : benchmark et release

## Lancer
```sh
go run ./cmd/serveur-http -port 8080 -dir ./mon-dossier
curl -i http://127.0.0.1:8080/
```

## Tests
```sh
go test -race ./...   # -race demande cgo (gcc) sur Windows
```
