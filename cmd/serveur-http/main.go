// Point d'entrée du programme : lit les options, ouvre la socket et lance le serveur.
// Utilisé par : `go run ./cmd/serveur-http` ou le binaire compilé.
// Toute la logique est dans internal/ : ce fichier ne fait que brancher les morceaux.
package main

import (
	"flag"
	"log"
	"net"
	"path/filepath"
	"strconv"

	"github.com/PiotrGc/serveur-http/internal/files"
	"github.com/PiotrGc/serveur-http/internal/server"
)

func main() {
	// flag : le paquet standard pour lire les options de la ligne de commande, ex. -port 9000.
	port := flag.Int("port", 8080, "port d'écoute (1 à 65535)")
	dir := flag.String("dir", ".", "dossier à partager en lecture seule")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		log.Fatalf("port invalide : %d", *port)
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
	log.Printf("serveur-http partage %s (lecture seule) sur http://%s", abs, addr)

	srv := &server.Server{Handler: d.Serve}
	if err := srv.Serve(ln); err != nil {
		log.Fatal(err)
	}
}
