// Petit client de charge : il mesure combien de requêtes par seconde le serveur tient, avec et sans
// keep-alive. Lancement : `go run ./cmd/bench` (options -n et -c).
// Il démarre lui-même un serveur sur 127.0.0.1, dans le même programme, qui sert un fichier de 1 Kio :
// la mesure se refait à l'identique sur n'importe quel PC, sans rien préparer.
// Le client utilise net/http : c'est permis côté client (comme dans les tests). L'interdiction ne
// concerne que le serveur, qui reste le nôtre.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PiotrGc/serveur-http/internal/files"
	"github.com/PiotrGc/serveur-http/internal/server"
)

func main() {
	n := flag.Int("n", 5000, "nombre de requêtes par mesure")
	c := flag.Int("c", 50, "nombre de clients en parallèle")
	flag.Parse()
	if *n < 1 || *c < 1 {
		log.Fatal("-n et -c doivent valoir au moins 1")
	}

	dir, err := os.MkdirTemp("", "serveur-http-bench")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	url, stop, err := startServer(dir, *c)
	if err != nil {
		log.Fatal(err)
	}
	defer stop()

	fmt.Printf("%d requêtes GET d'un fichier de 1 Kio, %d clients en parallèle\n\n", *n, *c)
	fmt.Println("| Mode | Requêtes/s | Durée | Échecs |")
	fmt.Println("|---|---|---|---|")
	for _, keepAlive := range []bool{true, false} {
		r := bench(url, *n, *c, keepAlive)
		mode := "keep-alive"
		if !keepAlive {
			mode = "une connexion par requête"
		}
		fmt.Printf("| %s | %.0f | %v | %d |\n", mode, float64(r.ok)/r.elapsed.Seconds(),
			r.elapsed.Round(time.Millisecond), r.fail)
	}
}

// startServer écrit le fichier de test dans dir et lance notre serveur sur un port libre.
// Les limites sont relevées à 2×c : tous les clients du bench viennent de 127.0.0.1, et la limite par
// IP par défaut (10) en refuserait la plupart avec des 503. Pourquoi 2×c et pas c : net/http ouvre
// parfois une connexion de plus que de clients (une connexion de réserve, ou la reconnexion après la
// 100ᵉ requête, avant que le serveur ait rendu la place de l'ancienne). Avec une limite à c tout
// juste, le premier essai a reçu 12 réponses 503 sur 5000.
// Le journal est coupé : écrire une ligne par requête mesurerait surtout la vitesse du terminal.
func startServer(dir string, c int) (url string, stop func(), err error) {
	if err := os.WriteFile(filepath.Join(dir, "petit.txt"), []byte(strings.Repeat("a", 1024)), 0o600); err != nil {
		return "", nil, err
	}
	d, err := files.Open(dir)
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0") // port 0 : le système choisit un port libre
	if err != nil {
		d.Close()
		return "", nil, err
	}
	srv := &server.Server{Handler: d.Serve, MaxConns: 2 * c, MaxConnsPerIP: 2 * c, Logf: func(string, ...any) {}}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String() + "/petit.txt", func() { ln.Close(); d.Close() }, nil
}

// result : le bilan d'une mesure.
type result struct {
	ok, fail int64
	elapsed  time.Duration
}

// bench envoie n requêtes GET à url avec c goroutines en parallèle, et compte les réussites (200).
// keepAlive=false force une nouvelle connexion TCP par requête : c'est le coût qu'évite le keep-alive
// (poignée de main TCP, nouvelle goroutine côté serveur, fermeture en douceur).
func bench(url string, n, c int, keepAlive bool) result {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives:   !keepAlive,
			MaxIdleConnsPerHost: c,    // sinon net/http ne garde que 2 connexions en réserve
			DisableCompression:  true, // on mesure le serveur, pas gzip
		},
	}
	defer client.CloseIdleConnections()

	// Chaque goroutine prend le numéro de requête suivant avec un compteur atomique (sans verrou),
	// jusqu'à n. sync/atomic : des opérations qu'aucune autre goroutine ne peut couper en deux.
	var next, ok, fail atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for range c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for next.Add(1) <= int64(n) {
				resp, err := client.Get(url)
				if err != nil {
					fail.Add(1)
					continue
				}
				// Lire tout le corps : sinon la connexion ne peut pas resservir (keep-alive).
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					ok.Add(1)
				} else {
					fail.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	return result{ok: ok.Load(), fail: fail.Load(), elapsed: time.Since(start)}
}
