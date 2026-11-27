// Tests d'intégration du serveur (V0.1) : un vrai serveur écoute sur un port libre,
// et on lui parle avec une socket brute (net.Dial) puis avec le client officiel net/http.
package server

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

var dateFixe = time.Date(2026, 11, 22, 15, 4, 5, 0, time.UTC)

// demarrer lance un serveur « Hello » sur 127.0.0.1 et un port choisi par le système,
// et rend son adresse. Le serveur est arrêté à la fin du test (t.Cleanup).
func demarrer(t *testing.T) string {
	t.Helper()
	// Port 0 : le système choisit un port libre, donc les tests ne se gênent jamais entre eux.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: Hello, Now: func() time.Time { return dateFixe }}
	fini := make(chan error, 1)
	// Le serveur tourne dans une goroutine, pour que le test puisse continuer pendant ce temps.
	go func() { fini <- srv.Serve(ln) }()
	t.Cleanup(func() {
		ln.Close()
		if err := <-fini; err != nil {
			t.Errorf("Serve a rendu une erreur à l'arrêt : %v", err)
		}
	})
	return ln.Addr().String()
}

// echanger envoie des octets bruts au serveur et rend tout ce qu'il répond jusqu'à la fermeture.
func echanger(t *testing.T, addr, brut string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Garde-fou : si le serveur ne ferme jamais la connexion, le test échoue au lieu de bloquer.
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, brut); err != nil {
		t.Fatal(err)
	}
	// On ferme notre sens d'écriture : le serveur voit la fin du flux (EOF), comme avec un client
	// qui a fini d'envoyer. Sans ça, un client muet attendrait la réponse pendant que le serveur
	// attend la requête, et les deux resteraient bloqués.
	conn.(*net.TCPConn).CloseWrite()
	rep, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("lecture de la réponse : %v", err)
	}
	return string(rep)
}

// TestHelloOctets : la réponse exacte à un GET. Depuis la V0.4, HTTP/1.1 garde la connexion
// (keep-alive) ; elle se ferme ensuite parce que le client a fini d'envoyer (CloseWrite dans echanger).
func TestHelloOctets(t *testing.T) {
	addr := demarrer(t)
	rep := echanger(t, addr, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	attendu := "HTTP/1.1 200 OK\r\n" +
		"Connection: keep-alive\r\n" +
		"Content-Length: 6\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Date: Sun, 22 Nov 2026 15:04:05 GMT\r\n" +
		"Server: serveur-http\r\n" +
		"\r\n" +
		"Hello\n"
	if rep != attendu {
		t.Fatalf("obtenu :\n%q\nattendu :\n%q", rep, attendu)
	}
}

// TestClientOfficiel : le client net/http de Go comprend notre réponse.
// Le paquet net/http n'est utilisé que dans les tests, comme client.
func TestClientOfficiel(t *testing.T) {
	addr := demarrer(t)
	rep, err := http.Get("http://" + addr + "/cours")
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Body.Close()
	corps, _ := io.ReadAll(rep.Body)
	if rep.StatusCode != 200 || string(corps) != "Hello\n" {
		t.Fatalf("statut %d, corps %q", rep.StatusCode, corps)
	}
	if rep.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type %q", rep.Header.Get("Content-Type"))
	}
}

// TestRequeteInvalide : chaque erreur du parser donne le bon statut, puis la connexion se ferme.
func TestRequeteInvalide(t *testing.T) {
	addr := demarrer(t)
	cas := map[string]string{
		"n'importe quoi\r\n\r\n":                              "HTTP/1.1 400 Bad Request\r\n",
		"BREW / HTTP/1.1\r\nHost: a\r\n\r\n":                  "HTTP/1.1 501 Not Implemented\r\n",
		"GET / HTTP/2.0\r\nHost: a\r\n\r\n":                   "HTTP/1.1 505 HTTP Version Not Supported\r\n",
		"GET /" + strings.Repeat("a", 9000) + " HTTP/1.1\r\n": "HTTP/1.1 414 URI Too Long\r\n",
	}
	for brut, debut := range cas {
		rep := echanger(t, addr, brut)
		if !strings.HasPrefix(rep, debut) {
			t.Errorf("pour %.30q : obtenu %.60q, attendu le début %q", brut, rep, debut)
		}
		if !strings.Contains(rep, "Connection: close\r\n") {
			t.Errorf("pour %.30q : Connection: close manquant", brut)
		}
	}
}

// TestReponseErreurPasPerdue : le client envoie 100 Kio alors que le serveur refuse dès 8 Kio (414).
// Sans fermeture « en douceur » (lingeringClose), le serveur fermerait avec des octets non lus,
// le système enverrait un reset (RST) et le client perdrait la réponse 414.
func TestReponseErreurPasPerdue(t *testing.T) {
	addr := demarrer(t)
	brut := "GET /" + strings.Repeat("a", 100<<10) + " HTTP/1.1\r\nHost: a\r\n\r\n"
	for i := 0; i < 5; i++ { // plusieurs essais : un reset dépend du timing
		rep := echanger(t, addr, brut)
		if !strings.HasPrefix(rep, "HTTP/1.1 414 URI Too Long\r\n") {
			t.Fatalf("essai %d : réponse 414 perdue, obtenu %.60q", i+1, rep)
		}
	}
}

// TestClientMuet : un client qui se connecte puis ferme sans rien envoyer ne reçoit rien,
// et le serveur continue de servir les clients suivants.
func TestClientMuet(t *testing.T) {
	addr := demarrer(t)
	if rep := echanger(t, addr, ""); rep != "" {
		t.Fatalf("un client muet ne doit rien recevoir, obtenu %q", rep)
	}
	if rep := echanger(t, addr, "GET / HTTP/1.1\r\nHost: a\r\n\r\n"); !strings.HasPrefix(rep, "HTTP/1.1 200 OK") {
		t.Fatalf("le serveur ne répond plus après un client muet : %q", rep)
	}
}

// TestHandlerQuiPanique : si le handler plante, le client reçoit 500 et le serveur survit.
// Sans recover, un seul bug dans un handler arrêterait tout le programme.
func TestHandlerQuiPanique(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: func(*http1.Request) *http1.Response { panic("bug") }}
	go srv.Serve(ln)
	defer ln.Close()

	for i := 0; i < 2; i++ { // deux fois : le serveur doit survivre au premier plantage
		rep := echanger(t, ln.Addr().String(), "GET / HTTP/1.1\r\nHost: a\r\n\r\n")
		if !strings.HasPrefix(rep, "HTTP/1.1 500 Internal Server Error\r\n") {
			t.Fatalf("essai %d : obtenu %q", i+1, rep)
		}
	}
}
