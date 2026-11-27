// Point d'entrée du programme : lit les options, ouvre la socket, lance le serveur
// et l'arrête proprement sur Ctrl+C.
// Utilisé par : `go run ./cmd/serveur-http` ou le binaire compilé.
// Toute la logique est dans internal/ : ce fichier ne fait que brancher les morceaux.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/PiotrGc/serveur-http/internal/auth"
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
	lan := flag.Bool("lan", false, "ouvrir au réseau local (Wi-Fi), avec un mot de passe tiré au hasard")
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

	handler := server.Handler(d.Serve)
	var password string
	if *lan {
		// Ouvert au Wi-Fi : n'importe quel appareil du réseau peut se connecter, donc mot de passe
		// obligatoire. Il change à chaque lancement : rien à retenir, rien qui traîne dans un fichier.
		if password, err = auth.NewPassword(); err != nil {
			log.Fatalf("impossible de tirer un mot de passe : %v", err)
		}
		handler = (&auth.Guard{Password: password}).Wrap(d.Serve)
	}

	addr := listenAddr(*lan, *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("impossible d'écouter sur %s : %v", addr, err)
	}
	printBanner(abs, *lan, *port, password)

	srv := &server.Server{Handler: handler, MaxConns: *maxConns}

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

// listenAddr : 127.0.0.1 par défaut (joignable seulement depuis ce PC), 0.0.0.0 avec -lan
// (toutes les cartes réseau, donc le Wi-Fi).
func listenAddr(lan bool, port int) string {
	host := "127.0.0.1"
	if lan {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// lanIPs rend les adresses IPv4 privées de ce PC (192.168.x.x, 10.x.x.x, 172.16-31.x.x) :
// ce sont elles qu'on tape sur le téléphone. IsPrivate exclut déjà 127.0.0.1 et les adresses publiques.
func lanIPs() []string {
	var ips []string
	// InterfaceAddrs liste les adresses de toutes les cartes réseau (Wi-Fi, Ethernet, VPN…).
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		// Assertion de type : une adresse de carte réseau est un *net.IPNet (adresse + masque).
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			ips = append(ips, n.IP.String())
		}
	}
	return ips
}

// printBanner affiche ce qu'il faut savoir au lancement : dossier, adresses, mot de passe.
func printBanner(dir string, lan bool, port int, password string) {
	log.Printf("serveur-http partage %s (lecture seule) — Ctrl+C pour arrêter", dir)
	if !lan {
		log.Printf("sur ce PC seulement : http://127.0.0.1:%d (option -lan pour le téléphone)", port)
		return
	}
	ips := lanIPs()
	if len(ips) == 0 {
		log.Print("aucune adresse de réseau local trouvée : le PC est-il connecté au Wi-Fi ?")
	}
	for _, ip := range ips {
		log.Printf("sur le téléphone (même Wi-Fi) : http://%s:%d", ip, port)
	}
	// fmt.Printf vers la sortie standard, sans la date : le mot de passe ne part pas dans le journal
	// si la sortie d'erreur est redirigée vers un fichier.
	fmt.Printf("\n  Mot de passe : %s   (nom d'utilisateur : n'importe lequel)\n\n", password)
	log.Print("sans HTTPS, ce mot de passe circule en clair sur le Wi-Fi : à utiliser chez toi, pas sur un Wi-Fi public")
}
