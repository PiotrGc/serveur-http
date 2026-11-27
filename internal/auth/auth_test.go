// Tests du mot de passe (Basic Auth) et du blocage après trop d'échecs (V0.7).
package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// horloge : une heure qu'on avance à la main, pour tester le blocage sans attendre une vraie minute.
type horloge struct{ t time.Time }

func (h *horloge) now() time.Time          { return h.t }
func (h *horloge) avancer(d time.Duration) { h.t = h.t.Add(d) }

func ok(*http1.Request) *http1.Response {
	return &http1.Response{Status: 200, Body: []byte("contenu protégé")}
}

// basic fabrique l'en-tête Authorization : « Basic » + base64(« utilisateur:mot de passe »).
func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func demander(h func(*http1.Request) *http1.Response, ip, authorization string) *http1.Response {
	headers := map[string]string{"host": "a"}
	if authorization != "" {
		headers["authorization"] = authorization
	}
	return h(&http1.Request{Method: "GET", Target: "/", Proto: "HTTP/1.1", Headers: headers, RemoteAddr: ip})
}

func nouveau() (*Guard, *horloge, func(*http1.Request) *http1.Response) {
	hl := &horloge{t: time.Date(2026, 11, 27, 20, 0, 0, 0, time.UTC)}
	g := &Guard{Password: "bon-mot-de-passe", Now: hl.now}
	return g, hl, g.Wrap(ok)
}

// TestSansMotDePasse : 401 avec WWW-Authenticate, pour que le navigateur affiche sa fenêtre.
func TestSansMotDePasse(t *testing.T) {
	_, _, h := nouveau()
	resp := demander(h, "192.168.1.30", "")
	if resp.Status != 401 || !strings.HasPrefix(resp.Headers["WWW-Authenticate"], `Basic realm="serveur-http"`) {
		t.Fatalf("statut %d, WWW-Authenticate %q", resp.Status, resp.Headers["WWW-Authenticate"])
	}
}

// TestBonMotDePasse : n'importe quel nom d'utilisateur, seul le mot de passe compte.
func TestBonMotDePasse(t *testing.T) {
	_, _, h := nouveau()
	for _, user := range []string{"piotr", "", "téléphone"} {
		if resp := demander(h, "192.168.1.30", basic(user, "bon-mot-de-passe")); resp.Status != 200 {
			t.Errorf("utilisateur %q : statut %d", user, resp.Status)
		}
	}
	if resp := demander(h, "192.168.1.30", "basic "+base64.StdEncoding.EncodeToString([]byte("x:bon-mot-de-passe"))); resp.Status != 200 {
		t.Errorf("« basic » en minuscules : statut %d (le schéma est insensible à la casse)", resp.Status)
	}
}

// TestMauvaisEnTetes : tout ce qui n'est pas un Basic valide avec le bon mot de passe donne 401.
func TestMauvaisEnTetes(t *testing.T) {
	_, _, h := nouveau()
	for _, a := range []string{
		basic("x", "mauvais"),
		basic("x", "bon-mot-de-passe "), // un espace de trop
		basic("x", "bon-mot-de-pass"),   // un caractère de moins
		"Basic !!!pas-du-base64",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("sans-deux-points")),
		"Bearer bon-mot-de-passe",
		"bon-mot-de-passe",
	} {
		if resp := demander(h, "192.168.1.30", a); resp.Status != 401 {
			t.Errorf("%q : statut %d, attendu 401", a, resp.Status)
		}
	}
}

// TestBlocageApresDixEchecs : 10 échecs en une minute → l'IP est bloquée (429), même avec le bon
// mot de passe, pendant une minute. Une autre IP n'est pas touchée.
func TestBlocageApresDixEchecs(t *testing.T) {
	_, hl, h := nouveau()
	for i := 0; i < 10; i++ {
		demander(h, "192.168.1.66", basic("x", "essai"))
	}
	resp := demander(h, "192.168.1.66", basic("x", "bon-mot-de-passe"))
	if resp.Status != 429 || resp.Headers["Retry-After"] == "" {
		t.Fatalf("après 10 échecs : statut %d, Retry-After %q, attendu 429", resp.Status, resp.Headers["Retry-After"])
	}
	if resp := demander(h, "192.168.1.30", basic("x", "bon-mot-de-passe")); resp.Status != 200 {
		t.Fatalf("une autre IP est bloquée aussi : statut %d", resp.Status)
	}
	hl.avancer(61 * time.Second)
	if resp := demander(h, "192.168.1.66", basic("x", "bon-mot-de-passe")); resp.Status != 200 {
		t.Fatalf("toujours bloquée après la fin du blocage : statut %d", resp.Status)
	}
}

// TestEchecsEspaces : 9 échecs, une minute d'attente, puis 9 autres : jamais 10 dans la même minute,
// donc pas de blocage. Et les requêtes sans mot de passe (la 1re de chaque navigateur) ne comptent pas.
func TestEchecsEspaces(t *testing.T) {
	_, hl, h := nouveau()
	for tour := 0; tour < 2; tour++ {
		for i := 0; i < 9; i++ {
			demander(h, "192.168.1.66", basic("x", "essai"))
			demander(h, "192.168.1.66", "") // sans mot de passe : ne compte pas
		}
		// Vérifié AVANT d'avancer l'horloge : sinon un blocage injustifié aurait déjà expiré,
		// et le test passerait pour une mauvaise raison.
		if resp := demander(h, "192.168.1.66", basic("x", "bon-mot-de-passe")); resp.Status != 200 {
			t.Fatalf("tour %d : bloquée après seulement 9 échecs dans la minute (statut %d)", tour+1, resp.Status)
		}
		hl.avancer(61 * time.Second)
	}
	if resp := demander(h, "192.168.1.66", basic("x", "bon-mot-de-passe")); resp.Status != 200 {
		t.Fatalf("bloquée alors qu'il n'y a jamais eu 10 échecs en une minute : statut %d", resp.Status)
	}
}

// TestNouveauMotDePasse : 12 caractères faciles à taper sur un téléphone (pas de 0/O ni de 1/l/I),
// et deux mots de passe tirés à la suite sont différents.
func TestNouveauMotDePasse(t *testing.T) {
	a, err := NewPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewPassword()
	if len(a) != 12 || a == b {
		t.Fatalf("mots de passe %q et %q", a, b)
	}
	if strings.Trim(a, alphabet) != "" || strings.ContainsAny(alphabet, "0O1lI") {
		t.Fatalf("caractère ambigu ou hors alphabet dans %q", a)
	}
}
