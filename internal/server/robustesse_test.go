// Tests de la V0.3 : plusieurs clients en même temps, timeouts, limite de connexions,
// arrêt propre et journal des requêtes.
package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

const requeteGET = "GET / HTTP/1.1\r\nHost: a\r\n\r\n"

// lancer démarre srv sur un port libre et rend son adresse. Le serveur est arrêté à la fin du test.
func lancer(t *testing.T, srv *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Logf == nil {
		srv.Logf = func(string, ...any) {} // silence : les tests n'ont pas besoin du journal
	}
	go srv.Serve(ln)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	return ln.Addr().String()
}

// TestConcurrence : 200 clients en parallèle, avec un handler qui prend 50 ms.
// Servis un par un, il faudrait 200 × 50 ms = 10 s. En parallèle, ça prend bien moins de 2 s.
func TestConcurrence(t *testing.T) {
	addr := lancer(t, &Server{Handler: func(r *http1.Request) *http1.Response {
		time.Sleep(50 * time.Millisecond)
		return Hello(r)
	}, MaxConns: 300, MaxConnsPerIP: 300}) // les 200 clients du test viennent tous de 127.0.0.1

	debut := time.Now()
	// sync.WaitGroup : attendre que les 200 goroutines aient fini.
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if strings.HasSuffix(echangerSansT(addr, requeteGET), "\r\n\r\nHello\n") {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := ok.Load(); n != 200 {
		t.Fatalf("%d réponses correctes sur 200", n)
	}
	if d := time.Since(debut); d > 2*time.Second {
		t.Fatalf("200 clients servis en %v : le serveur ne traite pas les connexions en parallèle", d)
	}
}

// echangerSansT : comme echanger, mais utilisable dans une goroutine (t.Fatal y est interdit).
// Rend "" en cas d'erreur.
func echangerSansT(addr, brut string) string {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, brut)
	conn.(*net.TCPConn).CloseWrite()
	rep, _ := io.ReadAll(conn)
	return string(rep)
}

// TestSlowloris : un client envoie un octet toutes les 50 ms sans jamais finir sa requête.
// Le délai de lecture est global pour les en-têtes : envoyer un octet ne le relance pas.
// Le client reçoit une 408 au bout de ReadTimeout, et pendant ce temps les autres sont servis.
func TestSlowloris(t *testing.T) {
	addr := lancer(t, &Server{Handler: Hello, ReadTimeout: 300 * time.Millisecond})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				// "GET /aaaa…" : une requête qui ne finit jamais
				if _, err := conn.Write([]byte("GET /aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"[i%60 : i%60+1])); err != nil {
					return
				}
			}
		}
	}()

	// Pendant l'attaque, un client normal est servi tout de suite.
	if rep := echanger(t, addr, requeteGET); !strings.HasPrefix(rep, "HTTP/1.1 200 OK") {
		t.Fatalf("client normal bloqué par le slowloris : %q", rep)
	}

	debut := time.Now()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	rep, _ := io.ReadAll(conn)
	if !strings.HasPrefix(string(rep), "HTTP/1.1 408 Request Timeout\r\n") {
		t.Fatalf("attendu une 408, obtenu %q", rep)
	}
	if d := time.Since(debut); d > 2*time.Second {
		t.Fatalf("slowloris coupé après %v, trop tard", d)
	}
}

// TestClientInactifCoupe : un client qui se connecte et n'envoie rien est déconnecté après ReadTimeout.
func TestClientInactifCoupe(t *testing.T) {
	addr := lancer(t, &Server{Handler: Hello, ReadTimeout: 200 * time.Millisecond})
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	debut := time.Now()
	io.ReadAll(conn) // rend quand le serveur ferme
	if d := time.Since(debut); d > 2*time.Second {
		t.Fatalf("client inactif gardé %v", d)
	}
}

// TestLimiteConnexions : avec MaxConns = 2, jamais plus de 2 requêtes dans le handler à la fois.
// Depuis la V0.7, le 3e client reçoit 503 au lieu d'attendre (voir TestPleinRepond503).
func TestLimiteConnexions(t *testing.T) {
	libere := make(chan struct{})
	var enCours atomic.Int32
	addr := lancer(t, &Server{MaxConns: 2, MaxConnsPerIP: 10, Handler: func(r *http1.Request) *http1.Response {
		enCours.Add(1)
		<-libere // bloque jusqu'à ce que le test libère
		return Hello(r)
	}})

	reponses := make(chan string, 3)
	for i := 0; i < 3; i++ {
		go func() { reponses <- echangerSansT(addr, requeteGET) }()
	}
	time.Sleep(300 * time.Millisecond)
	if n := enCours.Load(); n != 2 {
		t.Fatalf("%d requêtes dans le handler, attendu 2 (la limite)", n)
	}
	close(libere)
	var ok, refus int
	for i := 0; i < 3; i++ {
		switch rep := <-reponses; {
		case strings.HasPrefix(rep, "HTTP/1.1 200 OK"):
			ok++
		case strings.HasPrefix(rep, "HTTP/1.1 503 "):
			refus++
		default:
			t.Fatalf("réponse inattendue : %q", rep)
		}
	}
	if ok != 2 || refus != 1 {
		t.Fatalf("%d réponses 200 et %d refus 503, attendu 2 et 1", ok, refus)
	}
}

// lecteurLent : un « fichier » de n octets qui met un peu de temps à se lire, pour simuler un gros envoi.
type lecteurLent struct{ reste int }

func (l *lecteurLent) Read(p []byte) (int, error) {
	if l.reste == 0 {
		return 0, io.EOF
	}
	time.Sleep(10 * time.Millisecond)
	n := min(len(p), l.reste, 1000)
	for i := range n {
		p[i] = 'x'
	}
	l.reste -= n
	return n, nil
}

func (l *lecteurLent) Close() error { return nil }

// TestArretPropre : Shutdown laisse finir un téléchargement en cours, puis refuse les nouveaux clients.
func TestArretPropre(t *testing.T) {
	const taille = 30000 // 30 morceaux de 1000 octets × 10 ms ≈ 300 ms
	srv := &Server{Handler: func(*http1.Request) *http1.Response {
		return &http1.Response{Status: 200, File: &lecteurLent{reste: taille}, Size: taille}
	}}
	addr := lancer(t, srv)

	recu := make(chan string, 1)
	go func() { recu <- echangerSansT(addr, requeteGET) }()
	time.Sleep(100 * time.Millisecond) // le téléchargement est en cours

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown : %v", err)
	}
	rep := <-recu
	if !strings.HasSuffix(rep, strings.Repeat("x", taille)) {
		t.Fatalf("téléchargement coupé : %d octets reçus", len(rep))
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("le serveur accepte encore des connexions après Shutdown")
	}
}

// TestArretInactifsImmediat : Shutdown n'attend pas les clients qui n'ont rien envoyé.
func TestArretInactifsImmediat(t *testing.T) {
	srv := &Server{Handler: Hello, ReadTimeout: 10 * time.Second}
	addr := lancer(t, srv)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	debut := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown : %v", err)
	}
	if d := time.Since(debut); d > time.Second {
		t.Fatalf("Shutdown a attendu %v un client inactif", d)
	}
	// Pendant un arrêt, un client inactif est fermé sans recevoir de 408 : ce n'est pas sa faute.
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if rep, _ := io.ReadAll(conn); len(rep) != 0 {
		t.Fatalf("le client inactif a reçu %q pendant l'arrêt", rep)
	}
}

// TestArretForce : si un handler ne finit jamais, Shutdown abandonne à l'échéance du contexte
// et ferme la connexion de force.
func TestArretForce(t *testing.T) {
	bloque := make(chan struct{})
	defer close(bloque)
	srv := &Server{Handler: func(r *http1.Request) *http1.Response { <-bloque; return Hello(r) }}
	addr := lancer(t, srv)
	fini := make(chan string, 1)
	go func() { fini <- echangerSansT(addr, requeteGET) }()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != context.DeadlineExceeded {
		t.Fatalf("attendu context.DeadlineExceeded, obtenu %v", err)
	}
	// La connexion doit avoir été fermée de force : le client voit la fin du flux tout de suite,
	// alors que le handler est toujours bloqué.
	select {
	case <-fini:
	case <-time.After(time.Second):
		t.Fatal("Shutdown a rendu la main sans fermer la connexion bloquée")
	}
}

// lecteurInfini : un corps sans fin, pour remplir les tampons d'un client qui ne lit pas.
type lecteurInfini struct{}

func (lecteurInfini) Read(p []byte) (int, error) { return len(p), nil }
func (lecteurInfini) Close() error               { return nil }

// TestEcritureBloqueeCoupee : un client qui demande un énorme fichier puis ne lit plus.
// Sans délai d'écriture, la goroutine resterait bloquée pour toujours. Avec WriteTimeout,
// elle abandonne, et Shutdown n'a plus rien à attendre.
func TestEcritureBloqueeCoupee(t *testing.T) {
	srv := &Server{WriteTimeout: 200 * time.Millisecond, Handler: func(*http1.Request) *http1.Response {
		return &http1.Response{Status: 200, File: lecteurInfini{}, Size: 1 << 40}
	}}
	addr := lancer(t, srv)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, requeteGET) // puis on ne lit jamais
	time.Sleep(1500 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("la connexion bloquée en écriture n'a pas été coupée : %v", err)
	}
}

// TestEcritureLenteMaisActive : le délai d'écriture se relance à chaque morceau envoyé.
// Un téléchargement long (ici 30 × 10 ms = 300 ms) passe donc avec un WriteTimeout de 100 ms,
// tant qu'il avance. C'est indispensable pour un rush de plusieurs Go sur le Wi-Fi.
func TestEcritureLenteMaisActive(t *testing.T) {
	const taille = 30000
	addr := lancer(t, &Server{WriteTimeout: 100 * time.Millisecond, Handler: func(*http1.Request) *http1.Response {
		return &http1.Response{Status: 200, File: &lecteurLent{reste: taille}, Size: taille}
	}})
	if rep := echanger(t, addr, requeteGET); !strings.HasSuffix(rep, strings.Repeat("x", taille)) {
		t.Fatalf("téléchargement coupé : %d octets", len(rep))
	}
}

// TestPanicHorsHandler : un panic ailleurs que dans le handler (ici dans Now, pendant l'écriture)
// ne fait pas tomber le serveur : le client suivant est servi.
func TestPanicHorsHandler(t *testing.T) {
	var appels atomic.Int32
	addr := lancer(t, &Server{Handler: Hello, Now: func() time.Time {
		if appels.Add(1) == 1 {
			panic("bug dans Now")
		}
		return time.Now()
	}})
	echanger(t, addr, requeteGET) // la première connexion est sacrifiée
	if rep := echanger(t, addr, requeteGET); !strings.HasPrefix(rep, "HTTP/1.1 200 OK") {
		t.Fatalf("le serveur ne répond plus après un panic : %q", rep)
	}
}

// TestJournal : une ligne par requête, avec méthode, chemin et statut.
func TestJournal(t *testing.T) {
	var mu sync.Mutex
	var lignes []string
	addr := lancer(t, &Server{Handler: Hello, Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lignes = append(lignes, fmt.Sprintf(format, args...))
	}})
	echanger(t, addr, "GET /cours/td1.pdf HTTP/1.1\r\nHost: a\r\n\r\n")
	echanger(t, addr, "BREW / HTTP/1.1\r\nHost: a\r\n\r\n")

	mu.Lock()
	defer mu.Unlock()
	tout := strings.Join(lignes, "\n")
	for _, attendu := range []string{"127.0.0.1 GET /cours/td1.pdf 200", "127.0.0.1 - - 501"} {
		if !strings.Contains(tout, attendu) {
			t.Errorf("journal sans %q :\n%s", attendu, tout)
		}
	}
}
