// Mot de passe (HTTP Basic Auth) et blocage des IP qui essaient trop de mots de passe.
// Utilisé par : cmd/serveur-http, seulement avec -lan. Le Guard enveloppe le handler des fichiers :
// une requête n'atteint les fichiers que si elle porte le bon mot de passe.
// Ne chiffre rien : sans HTTPS, le mot de passe passe en clair sur le Wi-Fi (voir le plan, V0.7).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// alphabet : les caractères du mot de passe. Pas de 0/O ni de 1/l/I, qu'on confond en les recopiant
// sur un téléphone. 31 caractères × 12 positions ≈ 59 bits de hasard : avec 10 essais par minute,
// il faudrait des milliards d'années pour le trouver.
const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NewPassword tire un mot de passe de 12 caractères avec crypto/rand.
// Pourquoi crypto/rand et pas math/rand : math/rand est prévisible (il suffit de connaître sa graine),
// crypto/rand vient du générateur sécurisé du système.
func NewPassword() (string, error) {
	b := make([]byte, 12)
	for i := range b {
		// rand.Int tire un nombre uniforme dans [0, len(alphabet)) : chaque caractère a la même chance.
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}

// Guard vérifie le mot de passe et bloque les IP qui se trompent trop souvent.
// Les valeurs à 0 prennent les valeurs par défaut (10 échecs par minute, blocage d'une minute).
type Guard struct {
	Password    string
	MaxFailures int              // échecs autorisés dans Window avant blocage
	Window      time.Duration    // fenêtre dans laquelle on compte les échecs
	Block       time.Duration    // durée du blocage
	Now         func() time.Time // nil = time.Now ; les tests y mettent une horloge qu'ils avancent

	mu    sync.Mutex
	fails map[string]*record // par IP
}

// record : les échecs d'une IP.
type record struct {
	count        int
	start        time.Time // début de la fenêtre de comptage
	blockedUntil time.Time
}

// Wrap rend un handler qui vérifie le mot de passe avant d'appeler next.
func (g *Guard) Wrap(next func(*http1.Request) *http1.Response) func(*http1.Request) *http1.Response {
	return func(req *http1.Request) *http1.Response {
		ip := req.RemoteAddr
		if wait := g.blocked(ip); wait > 0 {
			return tooMany(wait)
		}
		header, present := req.Headers["authorization"]
		if !present {
			// La première requête d'un navigateur n'a jamais de mot de passe : ce n'est pas un échec,
			// c'est notre 401 qui lui fait afficher sa fenêtre de connexion.
			return unauthorized()
		}
		if !g.check(header) {
			if wait := g.fail(ip); wait > 0 {
				return tooMany(wait)
			}
			return unauthorized()
		}
		return next(req)
	}
}

// check dit si l'en-tête Authorization porte le bon mot de passe (n'importe quel nom d'utilisateur).
// Format (RFC 7617) : « Basic » + base64(« utilisateur:mot de passe »).
func (g *Guard) check(header string) bool {
	scheme, encoded, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return false
	}
	_, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false
	}
	// Comparaison en temps constant : avec ==, la comparaison s'arrête au premier caractère faux,
	// et en mesurant le temps de réponse on pourrait deviner le mot de passe lettre par lettre.
	// On compare les empreintes SHA-256 (toujours 32 octets), pour ne pas révéler non plus la longueur.
	a, b := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(g.Password))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// blocked rend le temps de blocage restant pour ip (0 si elle n'est pas bloquée).
func (g *Guard) blocked(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r := g.fails[ip]; r != nil {
		return r.blockedUntil.Sub(g.now())
	}
	return 0
}

// fail compte un échec pour ip. Rend la durée de blocage si cet échec déclenche le blocage, sinon 0.
func (g *Guard) fail(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.fails == nil {
		g.fails = map[string]*record{}
	}
	r := g.fails[ip]
	// Pas encore d'échec, ou fenêtre précédente terminée : on recommence à compter.
	// shortcut: une entrée par IP reste en mémoire jusqu'à son prochain échec ; sans importance sur un
	// réseau local, à purger régulièrement si le serveur était exposé à Internet.
	if r == nil || now.Sub(r.start) >= orDefault(g.Window, time.Minute) {
		r = &record{start: now}
		g.fails[ip] = r
	}
	r.count++
	if r.count >= orDefault(g.MaxFailures, 10) {
		block := orDefault(g.Block, time.Minute)
		r.blockedUntil = now.Add(block)
		r.count, r.start = 0, now
		return block
	}
	return 0
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// unauthorized : 401 avec WWW-Authenticate, l'en-tête qui fait afficher au navigateur sa fenêtre
// « nom d'utilisateur / mot de passe » (RFC 7617).
func unauthorized() *http1.Response {
	resp := http1.ErrorResponse(401)
	resp.Headers["WWW-Authenticate"] = `Basic realm="serveur-http", charset="UTF-8"`
	return resp
}

// tooMany : 429, avec Retry-After en secondes (au moins 1).
func tooMany(wait time.Duration) *http1.Response {
	resp := http1.ErrorResponse(429)
	resp.Headers["Retry-After"] = strconv.Itoa(max(1, int(wait.Seconds()+0.999)))
	return resp
}

// orDefault rend v, ou def si v vaut zéro.
func orDefault[T time.Duration | int](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}
