# serveur-http

Un serveur HTTP/1.1 écrit en Go **sans `net/http`**, directement sur une socket TCP, pour comprendre ce que les librairies cachent : parsing des octets, concurrence, keep-alive, requêtes Range, gzip et chunked.

Son usage : partager un dossier en lecture seule sur le Wi-Fi pour lire ses PDF ou ses vidéos depuis un téléphone.

## État
- [x] **V0.0** : le parser de requêtes (RFC 9112), avec les limites de taille et des refus stricts (400 / 414 / 431 / 501 / 505)
- [ ] V0.1 : premier client TCP
- [ ] V0.2 : servir un dossier
- [ ] V0.3 : concurrence et robustesse
- [ ] V0.4 : keep-alive
- [ ] V0.5 : gros fichiers et Range
- [ ] V0.6 : gzip et chunked
- [ ] V0.7 : accès Wi-Fi avec mot de passe
- [ ] V1.0 : benchmark et release

## Tests
```sh
go test ./...
```
