// Tests de la V0.7 côté serveur : limite par IP, 503 quand tout est plein, débit minimum, IP du client.
package server

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// bloquant : un handler qui attend qu'on le libère, pour garder des connexions occupées.
func bloquant(libere chan struct{}) Handler {
	return func(r *http1.Request) *http1.Response { <-libere; return Hello(r) }
}

// dialDepuis ouvre une connexion depuis une adresse locale précise (ex. 127.0.0.2), pour simuler
// une autre machine. Toute l'étendue 127.0.0.0/8 est une adresse de bouclage.
func dialDepuis(t *testing.T, ip, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}, Timeout: 2 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Skipf("impossible de se connecter depuis %s : %v", ip, err)
	}
	return c
}

// envoyer écrit une requête et rend la ligne de statut reçue (ou "" si rien n'arrive dans le délai).
func envoyer(c net.Conn, d time.Duration) string {
	c.SetDeadline(time.Now().Add(d))
	io.WriteString(c, requeteGET)
	b := make([]byte, 64)
	n, _ := io.ReadAtLeast(c, b, 12)
	return string(b[:n])
}

// TestLimiteParIP : la 3e connexion d'une même IP (limite 2) reçoit 503 tout de suite,
// et une autre IP est toujours servie.
func TestLimiteParIP(t *testing.T) {
	libere := make(chan struct{})
	defer close(libere)
	addr := lancer(t, &Server{Handler: bloquant(libere), MaxConnsPerIP: 2, MaxConns: 10})

	for i := 0; i < 2; i++ {
		c := dialDepuis(t, "127.0.0.1", addr)
		defer c.Close()
		go envoyer(c, 5*time.Second) // ces deux-là restent dans le handler
	}
	time.Sleep(200 * time.Millisecond)

	c3 := dialDepuis(t, "127.0.0.1", addr)
	defer c3.Close()
	if rep := envoyer(c3, 2*time.Second); !strings.HasPrefix(rep, "HTTP/1.1 503 ") {
		t.Fatalf("3e connexion de la même IP : %q, attendu 503", rep)
	}

	// L'autre IP n'est pas refusée : sa requête entre dans le handler (qui la bloque), donc rien
	// n'arrive avant le délai. Recevoir quoi que ce soit (un 503) serait l'erreur.
	autre := dialDepuis(t, "127.0.0.2", addr)
	defer autre.Close()
	if rep := envoyer(autre, 500*time.Millisecond); rep != "" {
		t.Fatalf("une autre IP reçoit %q alors que seule la 1re a atteint sa limite", rep)
	}
}

// TestLimiteParIPLibereLaPlace : une connexion fermée rend sa place à son IP. Avec une limite de 2,
// 5 connexions l'une après l'autre sont toutes servies.
func TestLimiteParIPLibereLaPlace(t *testing.T) {
	addr := lancer(t, &Server{Handler: Hello, MaxConnsPerIP: 2})
	for i := 0; i < 5; i++ {
		if rep := echanger(t, addr, requeteGET); !strings.HasPrefix(rep, "HTTP/1.1 200 OK") {
			t.Fatalf("connexion %d : %.40q (la place des précédentes n'a pas été rendue)", i+1, rep)
		}
		time.Sleep(50 * time.Millisecond) // laisse au serveur le temps de libérer la place
	}
}

// TestPleinRepond503 : quand toutes les places sont prises, le client suivant reçoit 503 tout de
// suite au lieu d'attendre. Un client lent ne peut plus bloquer la file d'attente de tout le monde.
func TestPleinRepond503(t *testing.T) {
	libere := make(chan struct{})
	defer close(libere)
	addr := lancer(t, &Server{Handler: bloquant(libere), MaxConns: 2, MaxConnsPerIP: 10})
	for i := 0; i < 2; i++ {
		c := dialDepuis(t, "127.0.0.1", addr)
		defer c.Close()
		go envoyer(c, 5*time.Second)
	}
	time.Sleep(200 * time.Millisecond)

	c := dialDepuis(t, "127.0.0.1", addr)
	defer c.Close()
	debut := time.Now()
	rep := envoyer(c, 3*time.Second)
	if !strings.HasPrefix(rep, "HTTP/1.1 503 ") || time.Since(debut) > time.Second {
		t.Fatalf("serveur plein : %q après %v, attendu 503 immédiat", rep, time.Since(debut))
	}
}

// TestDebitMinimum : un client qui lit très lentement (mais assez pour ne jamais dépasser le délai
// d'écriture) est coupé dès que son débit passe sous MinRate.
// On vérifie la coupure côté serveur (son journal) : côté client, les données déjà parties dans les
// tampons réseau (plusieurs Mo sous Windows) continuent d'arriver bien après la coupure.
func TestDebitMinimum(t *testing.T) {
	coupe := make(chan string, 10)
	addr := lancer(t, &Server{
		WriteTimeout: 10 * time.Second, MinRate: 64 << 10, RateWindow: 500 * time.Millisecond,
		Logf: func(format string, args ...any) {
			if l := fmt.Sprintf(format, args...); strings.Contains(l, errTooSlow.Error()) {
				coupe <- l
			}
		},
		Handler: func(*http1.Request) *http1.Response {
			return &http1.Response{Status: 200, File: lecteurInfini{}, Size: 1 << 30}
		},
	})
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(4096)
	io.WriteString(conn, requeteGET)

	// Le client lit ~20 Kio/s, sous les 64 Kio/s demandés, jusqu'à ce que le serveur le coupe.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		b := make([]byte, 1024)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				if _, err := conn.Read(b); err != nil {
					return
				}
			}
		}
	}()
	select {
	case <-coupe:
	case <-time.After(8 * time.Second):
		t.Fatal("un client à ~20 Kio/s n'a pas été coupé avec un débit minimum de 64 Kio/s")
	}
}

// TestDebitNormalPasCoupe : un client qui lit vite n'est jamais coupé, même avec un débit minimum
// élevé et des fenêtres courtes. 32 Mio doivent arriver en entier.
func TestDebitNormalPasCoupe(t *testing.T) {
	const taille = 32 << 20
	addr := lancer(t, &Server{
		MinRate: 1 << 20, RateWindow: 200 * time.Millisecond, // 1 Mio/s minimum
		Handler: func(*http1.Request) *http1.Response {
			return &http1.Response{Status: 200, File: &lecteurZeros{reste: taille}, Size: taille}
		},
	})
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: a\r\nConnection: close\r\n\r\n")
	n, _ := io.Copy(io.Discard, conn)
	if n < taille {
		t.Fatalf("téléchargement rapide coupé : %d octets sur %d", n, taille)
	}
}

// TestIPDuClient : le handler connaît l'IP du client (pour le blocage par IP de l'authentification).
func TestIPDuClient(t *testing.T) {
	ipVue := make(chan string, 1)
	addr := lancer(t, &Server{Handler: func(r *http1.Request) *http1.Response { ipVue <- r.RemoteAddr; return Hello(r) }})
	echanger(t, addr, requeteGET)
	if ip := <-ipVue; ip != "127.0.0.1" {
		t.Fatalf("RemoteAddr = %q, attendu 127.0.0.1 (sans le port)", ip)
	}
}
