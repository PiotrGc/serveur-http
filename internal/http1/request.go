// Parser des requêtes HTTP/1.1 : il transforme les octets reçus sur la socket en Request.
// Utilisé par : le serveur (à partir de la V0.1), une fois par requête reçue.
// Ne lit pas le corps de la requête et ne touche pas au réseau : il lit un io.Reader,
// ce qui permet de le tester avec une simple chaîne de caractères.
// Toutes les règles appliquées viennent des RFC 9112 et 9110, voir « 01 Entrées/Sources HTTP (C).md ».
package http1

import (
	"bufio"
	"io"
	"strings"
)

// Limites de taille. Un client qui les dépasse est refusé avant qu'il remplisse la mémoire.
const (
	MaxRequestLine = 8 << 10 // 8 Kio (8 << 10 = 8 × 1024) ; la RFC 9112 §3 recommande au moins 8 000 octets
	MaxHeaderLine  = 8 << 10 // 8 Kio par ligne d'en-tête
	MaxHeaders     = 100     // nombre maximum de lignes d'en-tête
)

// knownMethods : les méthodes définies par la RFC 9110 §9 (plus PATCH, RFC 5789).
// Une méthode hors de cette liste donne 501. Une méthode de la liste passe le parser,
// même si le serveur ne la gère pas : c'est lui qui répondra 405 plus tard (V0.2).
// Une map[string]bool sert ici d'ensemble : knownMethods["GET"] vaut true, une clé absente vaut false.
var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "DELETE": true,
	"CONNECT": true, "OPTIONS": true, "TRACE": true, "PATCH": true,
}

// Request est une requête HTTP déjà découpée.
// Headers a ses noms en minuscules, puisque les noms d'en-têtes ne sont pas sensibles à la casse
// (RFC 9110 §5.1) : on cherche toujours req.Headers["host"], jamais "Host".
type Request struct {
	Method  string            // "GET", "HEAD"…
	Target  string            // la cible telle qu'envoyée, ex. "/cours/td1.pdf" (pas encore décodée)
	Proto   string            // "HTTP/1.1" ou "HTTP/1.0"
	Headers map[string]string // nom en minuscules → valeur (les répétitions sont fusionnées avec ", ")
}

// NewReader enveloppe une connexion dans un lecteur avec tampon.
// Pourquoi un tampon : on lit octet par octet pour trouver les fins de ligne. Sans tampon,
// chaque octet coûterait un appel système (une lecture sur la socket), ce qui est très lent.
// Le serveur devra garder ce même lecteur pour toute la connexion : le tampon peut déjà
// contenir le début du corps ou de la requête suivante.
func NewReader(r io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(r, 4096)
}

// ParseRequest lit une requête complète, jusqu'à la ligne vide qui suit les en-têtes.
// Si r n'est pas déjà un *bufio.Reader, il est enveloppé. Ce qui suit les en-têtes
// (le corps) n'est alors plus récupérable : passe un *bufio.Reader si tu en as besoin.
// Rend io.EOF si le client a fermé la connexion sans rien envoyer (ce n'est pas une erreur de syntaxe),
// et une *ParseError avec le code de statut à renvoyer si la requête est invalide.
func ParseRequest(r io.Reader) (*Request, error) {
	// Assertion de type : si r est déjà un *bufio.Reader, ok vaut true et on le réutilise tel quel.
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = NewReader(r)
	}

	line, err := readLine(br, MaxRequestLine, errURITooLong)
	if err != nil {
		return nil, err // io.EOF (connexion fermée proprement) ou ParseError
	}
	req, err := parseRequestLine(line)
	if err != nil {
		return nil, err
	}

	req.Headers, err = readHeaders(br)
	if err != nil {
		return nil, err
	}

	// RFC 9112 §3.2 : en HTTP/1.1, exactement un en-tête Host est obligatoire.
	// Les doublons sont déjà refusés dans readHeaders. HTTP/1.0 n'a pas cette règle.
	if _, has := req.Headers["host"]; req.Proto == "HTTP/1.1" && !has {
		return nil, errBadRequest("en-tête Host manquant")
	}
	return req, nil
}

// readLine lit une ligne terminée par CRLF et la rend sans le CRLF.
// max : taille maximale de la ligne, CRLF non compris. Au-delà, on s'arrête tout de suite
// (sans lire la suite) et on rend l'erreur fabriquée par tooLong (414 ou 431 selon la ligne).
// Rend io.EOF si le flux se termine avant le premier octet, et 400 s'il se termine au milieu.
func readLine(br *bufio.Reader, max int, tooLong func(string) error) (string, error) {
	// strings.Builder construit une chaîne morceau par morceau sans recopier à chaque ajout.
	var sb strings.Builder
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF && sb.Len() == 0 {
				return "", io.EOF
			}
			if err == io.EOF {
				return "", errBadRequest("flux coupé au milieu d'une ligne")
			}
			return "", err // erreur réseau (timeout, connexion coupée) : on la remonte telle quelle
		}

		switch b {
		case '\n':
			// RFC 9112 §2.2 : on exige CRLF. Un LF seul (sans CR juste avant) est refusé.
			return "", errBadRequest("fin de ligne LF sans CR")
		case '\r':
			// Un CR doit être suivi immédiatement d'un LF. Sinon, c'est un CR seul : refusé.
			next, err := br.ReadByte()
			if err == io.EOF {
				return "", errBadRequest("flux coupé après un CR")
			}
			if err != nil {
				return "", err
			}
			if next != '\n' {
				return "", errBadRequest("CR seul au milieu d'une ligne")
			}
			return sb.String(), nil
		}

		// Le contrôle est fait à chaque octet : un client qui envoie 1 Go sans fin de ligne
		// est arrêté après max+1 octets, sans que le serveur garde tout en mémoire.
		if sb.Len() == max {
			return "", tooLong("ligne trop longue")
		}
		sb.WriteByte(b)
	}
}

// parseRequestLine découpe « MÉTHODE CIBLE VERSION » (RFC 9112 §3).
func parseRequestLine(line string) (*Request, error) {
	// strings.Split avec un seul espace : « GET  / » (deux espaces) donne une partie vide, refusée plus bas.
	parts := strings.Split(line, " ")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, errBadRequest("ligne de requête mal formée")
	}
	method, target, proto := parts[0], parts[1], parts[2]

	// La syntaxe d'abord (400), puis la sémantique (501) : « GE(T » est mal écrit,
	// alors que « BREW » est bien écrit mais inconnu.
	if !isToken(method) {
		return nil, errBadRequest("méthode mal formée")
	}
	if !knownMethods[method] {
		return nil, errNotImplemented("méthode inconnue : " + method)
	}

	// On n'accepte que la forme « origin-form » (/chemin), et « * » pour OPTIONS (RFC 9112 §3.2).
	// Les autres formes (http://hôte/…, hôte:port) servent aux proxys, ce que ce serveur n'est pas.
	if target == "*" {
		if method != "OPTIONS" {
			return nil, errBadRequest("la cible * est réservée à OPTIONS")
		}
	} else if !strings.HasPrefix(target, "/") {
		return nil, errBadRequest("la cible doit commencer par /")
	}
	// RFC 3986 : une URI ne contient que de l'ASCII visible (0x21 à 0x7e). Les accents et les espaces
	// arrivent encodés (%C3%A9, %20) : un navigateur le fait toujours. On refuse donc les caractères de
	// contrôle (dont \x1b, qui commence les séquences d'échappement d'un terminal et pourrait falsifier
	// le journal), DEL et les octets non ASCII bruts.
	for i := 0; i < len(target); i++ {
		if target[i] < 0x21 || target[i] > 0x7e {
			return nil, errBadRequest("caractère interdit dans la cible")
		}
	}

	if err := checkVersion(proto); err != nil {
		return nil, err
	}
	return &Request{Method: method, Target: target, Proto: proto}, nil
}

// checkVersion vérifie « HTTP/x.y » (RFC 9112 §2.3, sensible à la casse).
// Une version bien écrite mais autre que 1.0 ou 1.1 donne 505, une version mal écrite donne 400.
func checkVersion(proto string) error {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	if len(proto) != 8 || !strings.HasPrefix(proto, "HTTP/") ||
		!isDigit(proto[5]) || proto[6] != '.' || !isDigit(proto[7]) {
		return errBadRequest("version mal formée : " + proto)
	}
	if proto != "HTTP/1.1" && proto != "HTTP/1.0" {
		return errVersionNotSupported("version non gérée : " + proto)
	}
	return nil
}

// readHeaders lit les lignes « Nom: valeur » jusqu'à la ligne vide.
// Rend une map nom en minuscules → valeur, avec les répétitions fusionnées (RFC 9110 §5.3).
func readHeaders(br *bufio.Reader) (map[string]string, error) {
	headers := map[string]string{}
	for count := 0; ; count++ {
		line, err := readLine(br, MaxHeaderLine, errHeadersTooLarge)
		if err == io.EOF {
			return nil, errBadRequest("flux coupé avant la fin des en-têtes")
		}
		if err != nil {
			return nil, err
		}
		if line == "" {
			return headers, nil // ligne vide : fin des en-têtes
		}
		if count == MaxHeaders {
			return nil, errHeadersTooLarge("trop d'en-têtes")
		}

		// RFC 9112 §5.2 : une ligne qui commence par un espace ou une tabulation continue la précédente
		// (« obs-fold »). C'est obsolète et ça a servi à des attaques : on refuse.
		if line[0] == ' ' || line[0] == '\t' {
			return nil, errBadRequest("pliage de ligne interdit")
		}

		// strings.Cut coupe au premier « : » et rend (avant, après, trouvé).
		name, value, found := strings.Cut(line, ":")
		// isToken refuse aussi un nom vide et un espace avant « : » (RFC 9112 §5.1).
		if !found || !isToken(name) {
			return nil, errBadRequest("ligne d'en-tête mal formée")
		}
		// RFC 9110 §5.5 : on retire les espaces et tabulations autour de la valeur.
		value = strings.Trim(value, " \t")
		if !isValidValue(value) {
			return nil, errBadRequest("caractère interdit dans la valeur de " + name)
		}

		name = strings.ToLower(name)
		if old, seen := headers[name]; seen {
			// Deux Host rendent la requête ambiguë (à quel site est-elle destinée ?) : RFC 9112 §3.2.
			if name == "host" {
				return nil, errBadRequest("plusieurs en-têtes Host")
			}
			value = old + ", " + value
		}
		headers[name] = value
	}
}

// isToken dit si s est un « token » au sens de la RFC 9110 §5.6.2 :
// au moins un caractère, uniquement des lettres, des chiffres et !#$%&'*+-.^_`|~
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return false
		}
	}
	return true
}

// isValidValue refuse les caractères de contrôle (octets 0 à 31 et 127) sauf la tabulation.
// RFC 9110 §5.5 : CR, LF et NUL sont interdits ; on refuse tous les autres contrôles par prudence.
// Les octets 128 à 255 (UTF-8, accents) sont acceptés.
func isValidValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
