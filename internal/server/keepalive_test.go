// Tests de la V0.4 : plusieurs requêtes sur une même connexion (keep-alive).
package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// echo répond avec le chemin demandé dans le corps : on peut vérifier l'ordre des réponses.
func echo(r *http1.Request) *http1.Response {
	return &http1.Response{Status: 200, Headers: map[string]string{"Content-Type": "text/plain"}, Body: []byte(r.Target)}
}

// lireReponse lit UNE réponse sur la connexion, avec le lecteur de réponses de net/http
// (utilisé seulement dans les tests, comme client). Rend le statut, l'en-tête Connection et le corps.
func lireReponse(t *testing.T, br *bufio.Reader) (int, string, string) {
	t.Helper()
	rep, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("lecture de la réponse : %v", err)
	}
	corps, _ := io.ReadAll(rep.Body)
	rep.Body.Close()
	conn := "keep-alive"
	if rep.Close {
		conn = "close"
	}
	return rep.StatusCode, conn, string(corps)
}

func dial(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn, bufio.NewReader(conn)
}

// TestKeepAliveSequentiel : une requête, sa réponse, puis une deuxième sur la même connexion.
func TestKeepAliveSequentiel(t *testing.T) {
	conn, br := dial(t, lancer(t, &Server{Handler: echo}))
	for _, chemin := range []string{"/a", "/b"} {
		io.WriteString(conn, "GET "+chemin+" HTTP/1.1\r\nHost: a\r\n\r\n")
		statut, c, corps := lireReponse(t, br)
		if statut != 200 || c != "keep-alive" || corps != chemin {
			t.Fatalf("%s : statut %d, Connection %s, corps %q", chemin, statut, c, corps)
		}
	}
}

// TestKeepAlivePipeline : 3 requêtes envoyées d'un coup, 3 réponses dans le même ordre (RFC 9112 §9.3.2).
func TestKeepAlivePipeline(t *testing.T) {
	conn, br := dial(t, lancer(t, &Server{Handler: echo}))
	io.WriteString(conn, "GET /a HTTP/1.1\r\nHost: a\r\n\r\nGET /b HTTP/1.1\r\nHost: a\r\n\r\nGET /c HTTP/1.1\r\nHost: a\r\n\r\n")
	for _, chemin := range []string{"/a", "/b", "/c"} {
		if _, _, corps := lireReponse(t, br); corps != chemin {
			t.Fatalf("attendu %s, obtenu %q", chemin, corps)
		}
	}
}

// TestConnectionClose : le client demande la fermeture, le serveur répond puis ferme.
func TestConnectionClose(t *testing.T) {
	conn, br := dial(t, lancer(t, &Server{Handler: echo}))
	io.WriteString(conn, "GET /a HTTP/1.1\r\nHost: a\r\nConnection: close\r\n\r\n")
	if _, c, _ := lireReponse(t, br); c != "close" {
		t.Fatalf("Connection %s, attendu close", c)
	}
	if reste, _ := io.ReadAll(br); len(reste) != 0 {
		t.Fatalf("des octets après la réponse : %q", reste)
	}
}

// TestHTTP10 : HTTP/1.0 ferme par défaut, et garde la connexion si on le demande.
func TestHTTP10(t *testing.T) {
	addr := lancer(t, &Server{Handler: echo})
	if rep := echanger(t, addr, "GET /a HTTP/1.0\r\n\r\n"); !strings.Contains(rep, "Connection: close\r\n") {
		t.Fatalf("HTTP/1.0 sans keep-alive : %q", rep)
	}
	conn, br := dial(t, addr)
	for _, chemin := range []string{"/a", "/b"} {
		io.WriteString(conn, "GET "+chemin+" HTTP/1.0\r\nConnection: keep-alive\r\n\r\n")
		if _, c, corps := lireReponse(t, br); c != "keep-alive" || corps != chemin {
			t.Fatalf("HTTP/1.0 keep-alive %s : Connection %s, corps %q", chemin, c, corps)
		}
	}
}

// TestInactiviteEntreRequetes : après une réponse, une connexion inactive est fermée après IdleTimeout,
// sans 408 (ne rien envoyer entre deux requêtes est normal).
func TestInactiviteEntreRequetes(t *testing.T) {
	conn, br := dial(t, lancer(t, &Server{Handler: echo, IdleTimeout: 200 * time.Millisecond}))
	io.WriteString(conn, "GET /a HTTP/1.1\r\nHost: a\r\n\r\n")
	lireReponse(t, br)
	debut := time.Now()
	reste, _ := io.ReadAll(br)
	if len(reste) != 0 {
		t.Fatalf("reçu %q après l'inactivité, attendu une simple fermeture", reste)
	}
	if d := time.Since(debut); d > 2*time.Second {
		t.Fatalf("connexion inactive gardée %v", d)
	}
}

// TestMaxRequetesParConnexion : au-delà de la limite, la dernière réponse annonce la fermeture.
func TestMaxRequetesParConnexion(t *testing.T) {
	conn, br := dial(t, lancer(t, &Server{Handler: echo, MaxRequestsPerConn: 2}))
	io.WriteString(conn, "GET /a HTTP/1.1\r\nHost: a\r\n\r\nGET /b HTTP/1.1\r\nHost: a\r\n\r\nGET /c HTTP/1.1\r\nHost: a\r\n\r\n")
	if _, c, _ := lireReponse(t, br); c != "keep-alive" {
		t.Fatalf("1re réponse : Connection %s", c)
	}
	if _, c, corps := lireReponse(t, br); c != "close" || corps != "/b" {
		t.Fatalf("2e réponse : Connection %s, corps %q", c, corps)
	}
	conn.(*net.TCPConn).CloseWrite()
	if reste, _ := io.ReadAll(br); len(reste) != 0 {
		t.Fatalf("une 3e réponse est arrivée : %q", reste)
	}
}

// TestCorpsIgnoreAvantRequeteSuivante : un corps qu'on n'utilise pas est quand même lu.
// Sinon « hello » serait pris pour le début de la requête suivante (« helloGET /b … » → 400).
func TestCorpsIgnoreAvantRequeteSuivante(t *testing.T) {
	conn, br := dial(t, lancer(t, &Server{Handler: echo}))
	io.WriteString(conn, "POST /a HTTP/1.1\r\nHost: a\r\nContent-Length: 5\r\n\r\nhelloGET /b HTTP/1.1\r\nHost: a\r\n\r\n")
	lireReponse(t, br)
	if statut, _, corps := lireReponse(t, br); statut != 200 || corps != "/b" {
		t.Fatalf("2e requête : statut %d, corps %q", statut, corps)
	}
}

// TestCorpsTropGros : un corps au-delà de MaxDiscardBody n'est pas lu : on répond puis on ferme.
// Le client envoie 200 Kio : plus que le tampon du serveur (4 Kio), donc des octets restent non lus
// dans le système. Sans fermeture en douceur, le reset TCP ferait perdre la réponse au client.
func TestCorpsTropGros(t *testing.T) {
	addr := lancer(t, &Server{Handler: echo})
	for i := 0; i < 5; i++ { // plusieurs essais : un reset dépend du timing
		conn, br := dial(t, addr)
		io.WriteString(conn, "POST /a HTTP/1.1\r\nHost: a\r\nContent-Length: 10000000\r\n\r\n"+strings.Repeat("x", 200<<10))
		if _, c, _ := lireReponse(t, br); c != "close" {
			t.Fatalf("essai %d : Connection %s, attendu close", i+1, c)
		}
	}
}

// TestCorpsInvalide : les corps qu'on ne sait pas lire sans risque sont refusés, puis la connexion est fermée.
// Content-Length + Transfer-Encoding ensemble, c'est la base des attaques par « request smuggling ».
func TestCorpsInvalide(t *testing.T) {
	addr := lancer(t, &Server{Handler: echo})
	cas := map[string]string{
		"Transfer-Encoding: chunked\r\n":                      "HTTP/1.1 501 ",
		"Content-Length: 5\r\nTransfer-Encoding: chunked\r\n": "HTTP/1.1 400 ",
		"Content-Length: abc\r\n":                             "HTTP/1.1 400 ",
	}
	for entete, debut := range cas {
		rep := echanger(t, addr, "POST /a HTTP/1.1\r\nHost: a\r\n"+entete+"\r\nhelloGET /b HTTP/1.1\r\nHost: a\r\n\r\n")
		if !strings.HasPrefix(rep, debut) || !strings.Contains(rep, "Connection: close\r\n") {
			t.Errorf("%q : obtenu %.80q", entete, rep)
		}
		if strings.Count(rep, "HTTP/1.1 ") != 1 {
			t.Errorf("%q : la requête suivante a été traitée alors que la connexion devait fermer", entete)
		}
	}
}

// TestClientOfficielReutilise : le client net/http réutilise la connexion pour la 2e requête.
func TestClientOfficielReutilise(t *testing.T) {
	addr := lancer(t, &Server{Handler: echo})
	client := &http.Client{Timeout: 5 * time.Second}
	var reutilisee []bool
	// httptrace : des « crochets » appelés par le client pendant la requête. GotConn dit si la
	// connexion venait du pool (Reused) ou si une nouvelle a été ouverte.
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reutilisee = append(reutilisee, i.Reused) }}
	for _, chemin := range []string{"/a", "/b"} {
		r, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), "GET", "http://"+addr+chemin, nil)
		rep, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(rep.Body)
		rep.Body.Close()
	}
	if len(reutilisee) != 2 || reutilisee[0] || !reutilisee[1] {
		t.Fatalf("réutilisation des connexions : %v, attendu [false true]", reutilisee)
	}
}

// TestArretKeepAliveInactif : une connexion keep-alive qui attend sa prochaine requête
// n'empêche pas l'arrêt, et ne reçoit rien.
func TestArretKeepAliveInactif(t *testing.T) {
	srv := &Server{Handler: echo, IdleTimeout: 30 * time.Second}
	conn, br := dial(t, lancer(t, srv))
	io.WriteString(conn, "GET /a HTTP/1.1\r\nHost: a\r\n\r\n")
	lireReponse(t, br)

	debut := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown : %v", err)
	}
	if d := time.Since(debut); d > time.Second {
		t.Fatalf("Shutdown a attendu %v une connexion inactive", d)
	}
	if reste, _ := io.ReadAll(br); len(reste) != 0 {
		t.Fatalf("la connexion inactive a reçu %q", reste)
	}
}

// TestArretAnnonceFermeture : une réponse qui part pendant un arrêt annonce « Connection: close »,
// pour que le client n'essaie pas d'envoyer une requête suivante sur une connexion qui va fermer.
func TestArretAnnonceFermeture(t *testing.T) {
	libere := make(chan struct{})
	dansHandler := make(chan struct{})
	srv := &Server{Handler: func(r *http1.Request) *http1.Response {
		close(dansHandler)
		<-libere
		return echo(r)
	}}
	conn, br := dial(t, lancer(t, srv))
	io.WriteString(conn, "GET /a HTTP/1.1\r\nHost: a\r\n\r\n")
	<-dansHandler

	arret := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		arret <- srv.Shutdown(ctx)
	}()
	time.Sleep(100 * time.Millisecond) // Shutdown a commencé
	close(libere)

	if _, c, corps := lireReponse(t, br); c != "close" || corps != "/a" {
		t.Fatalf("Connection %s, corps %q : attendu close et /a", c, corps)
	}
	if err := <-arret; err != nil {
		t.Fatalf("Shutdown : %v", err)
	}
}
