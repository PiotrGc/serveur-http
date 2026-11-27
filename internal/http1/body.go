// Corps de requête et keep-alive : ce que le serveur doit savoir pour enchaîner plusieurs requêtes
// sur une même connexion.
// Utilisé par : internal/server (boucle keep-alive de handleConn).
// Ne lit pas le corps lui-même : il dit seulement combien d'octets il fait, ou s'il faut refuser.
package http1

import (
	"strconv"
	"strings"
)

// BodyLength rend la taille du corps de la requête, en octets (0 s'il n'y en a pas).
// Le serveur en a besoin même s'il n'utilise pas le corps : il doit le lire pour le sauter,
// sinon ses octets seraient pris pour le début de la requête suivante.
//
// Refus (RFC 9112 §6) :
//   - Content-Length ET Transfer-Encoding ensemble → 400. Deux serveurs qui ne choisissent pas le même
//     en-tête ne coupent pas la requête au même endroit : c'est la base du « request smuggling ».
//   - Transfer-Encoding seul → 501 : ce serveur ne lit pas les corps « chunked » (inutile en lecture seule).
//   - Content-Length qui n'est pas un simple nombre (signe, espace, deux valeurs « 5, 6 ») → 400.
func BodyLength(req *Request) (int64, error) {
	cl, hasCL := req.Headers["content-length"]
	_, hasTE := req.Headers["transfer-encoding"]
	switch {
	case hasCL && hasTE:
		return 0, errBadRequest("Content-Length et Transfer-Encoding ensemble")
	case hasTE:
		return 0, errNotImplemented("Transfer-Encoding non géré")
	case !hasCL:
		return 0, nil
	}
	// strconv.ParseInt accepte « +5 » : on vérifie d'abord qu'il n'y a que des chiffres.
	// 15 chiffres au maximum, soit moins de 1 Po : ça évite tout dépassement d'int64.
	if cl == "" || len(cl) > 15 || strings.Trim(cl, "0123456789") != "" {
		return 0, errBadRequest("Content-Length invalide")
	}
	n, _ := strconv.ParseInt(cl, 10, 64) // ne peut pas échouer après la vérification ci-dessus
	return n, nil
}

// WantsKeepAlive dit si le client veut garder la connexion après la réponse (RFC 9112 §9.3).
// HTTP/1.1 la garde par défaut, sauf « Connection: close ». HTTP/1.0 la ferme par défaut,
// sauf « Connection: keep-alive ». L'en-tête Connection est une liste : « keep-alive, close » = close.
func WantsKeepAlive(req *Request) bool {
	keep, close := false, false
	for _, opt := range strings.Split(req.Headers["connection"], ",") {
		switch strings.ToLower(strings.TrimSpace(opt)) {
		case "close":
			close = true
		case "keep-alive":
			keep = true
		}
	}
	if close {
		return false
	}
	return req.Proto == "HTTP/1.1" || keep
}
