// Point d'entrée du programme : lit les options, ouvre la socket, lance le serveur
// et l'arrête proprement sur Ctrl+C.
// Utilisé par : `go run ./cmd/serveur-http` ou le binaire compilé.
// Toute la logique est dans internal/ : ce fichier ne fait que brancher les morceaux.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/PiotrGc/serveur-http/internal/files"
	"github.com/PiotrGc/serveur-http/internal/server"
)

// shutdownTimeout : le temps laissé aux réponses en cours pour finir après Ctrl+C.
const shutdownTimeout = 5 * time.Second

func main() {
	// flag : le paquet standard pour lire les options de la ligne de commande, ex. -port 9000.
	port := flag.Int("port", 8080, "port d'écoute (1 à 65535)")
	dir := flag.String("dir", ".", "dossier à partager en lecture seule")
	maxConns := flag.Int("max-conns", server.DefaultMaxConns, "nombre maximum de connexions simultanées")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		log.Fatalf("port invalide : %d", *port)
	}
	if *maxConns < 1 {
		log.Fatalf("max-conns invalide : %d", *maxConns)
	}

	// Chemin absolu, pour afficher clairement quel dossier est partagé.
	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("dossier invalide : %v", err)
	}
	d, err := files.Open(abs)
	if err != nil {
		log.Fatalf("impossible d'ouvrir le dossier %s : %v", abs, err)
	}
	defer d.Close()

	// 127.0.0.1 seulement : le serveur n'est joignable que depuis ce PC.
	// L'ouverture au Wi-Fi se fera avec une option explicite en V0.7.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("impossible d'écouter sur %s : %v", addr, err)
	}
	log.Printf("serveur-http partage %s (lecture seule) sur http://%s — Ctrl+C pour arrêter", abs, addr)

	srv := &server.Server{Handler: d.Serve, MaxConns: *maxConns}

	// signal.NotifyContext rend un contexte qui est « annulé » quand le programme reçoit Ctrl+C
	// (os.Interrupt). Sans ça, Ctrl+C tuerait le programme net, en coupant les téléchargements en cours.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Serve tourne dans une goroutine. Le channel errc rapporte sa fin (erreur grave, ou fermeture).
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if err != nil {
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Printf("arrêt demandé : on laisse %v aux réponses en cours", shutdownTimeout)
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			log.Printf("arrêt forcé : des réponses n'avaient pas fini (%v)", err)
			return
		}
		log.Print("arrêt propre terminé")
	}
}
