// Le serveur TCP : il accepte les connexions, lit une requête, appelle le handler et écrit la réponse.
// Utilisé par : cmd/serveur-http (le programme), et les tests d'intégration.
// Ne sait pas ce qu'il sert : c'est le Handler qui décide de la réponse (« Hello » en V0.1,
// les fichiers d'un dossier en V0.2). Le parsing et l'écriture des octets sont dans internal/http1.
package server

import (
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// Handler reçoit une requête valide et rend la réponse à envoyer.
type Handler func(*http1.Request) *http1.Response

// Server relie une socket d'écoute à un Handler.
type Server struct {
	Handler Handler
	// Now donne l'heure de l'en-tête Date. nil = time.Now. Les tests y mettent une date fixe.
	Now func() time.Time
}

// Hello est le handler de la V0.1 : il répond « Hello » à toute requête valide.
func Hello(*http1.Request) *http1.Response {
	return &http1.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:    []byte("Hello\n"),
	}
}

// Serve accepte les connexions de ln une par une, jusqu'à ce que ln soit fermé.
// Rend nil quand ln est fermé normalement, et l'erreur sinon.
//
// shortcut: une seule connexion à la fois et pas de timeout, donc un client qui ne finit jamais
// sa requête bloque tous les autres. C'est corrigé en V0.3 (une goroutine par connexion + deadlines).
func (s *Server) Serve(ln net.Listener) error {
	for {
		// Accept attend (bloque) jusqu'à ce qu'un client se connecte.
		conn, err := ln.Accept()
		if err != nil {
			// net.ErrClosed : quelqu'un a fermé ln (arrêt du serveur ou fin d'un test). Ce n'est pas une panne.
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.handleConn(conn)
	}
}

// handleConn traite une connexion : une requête, une réponse, puis la fermeture.
func (s *Server) handleConn(conn net.Conn) {
	// defer : conn.Close() s'exécute à la sortie de la fonction, quel que soit le chemin pris.
	defer conn.Close()

	// Le même lecteur avec tampon doit servir pour toute la connexion (voir http1.NewReader).
	br := http1.NewReader(conn)
	req, err := http1.ParseRequest(br)

	var perr *http1.ParseError
	switch {
	case err == io.EOF:
		return // le client s'est connecté puis a fermé sans rien envoyer : rien à répondre
	case errors.As(err, &perr):
		s.write(conn, http1.ErrorResponse(perr.Status))
		log.Printf("%s refusée : %v", conn.RemoteAddr(), perr)
		lingeringClose(conn)
		return
	case err != nil:
		return // erreur réseau (connexion coupée) : impossible de répondre
	}

	resp := s.callHandler(req)
	// RFC 9110 §9.3.2 : HEAD rend exactement les en-têtes de GET, sans le corps.
	// C'est géré ici une fois pour toutes, plutôt que dans chaque handler.
	if req.Method == "HEAD" {
		resp.OmitBody = true
	}
	s.write(conn, resp)
}

// callHandler appelle le handler et transforme un plantage (panic) en réponse 500.
// Pourquoi : sans recover, un panic dans le handler arrête tout le programme, donc tous les clients.
func (s *Server) callHandler(req *http1.Request) (resp *http1.Response) {
	// recover() ne marche que dans une fonction lancée par defer. Elle arrête le panic en cours,
	// et on remplace alors la réponse nommée resp par une 500.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic dans le handler pour %s %s : %v", req.Method, req.Target, r)
			resp = http1.ErrorResponse(500)
		}
	}()
	return s.Handler(req)
}

// write envoie la réponse, puis ferme le fichier qu'elle lisait (s'il y en a un).
// Si un en-tête est invalide, rien n'a encore été envoyé : on peut répondre 500 à la place.
// Pour toute autre erreur (client parti, fichier tronqué), une partie est peut-être déjà partie :
// on n'écrit plus rien, et la connexion sera fermée par handleConn.
func (s *Server) write(conn net.Conn, resp *http1.Response) {
	if resp.File != nil {
		// Fermer le fichier dans tous les cas : sinon chaque requête garderait un descripteur ouvert.
		defer resp.File.Close()
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	err := http1.WriteResponse(conn, resp, now())
	if err == nil {
		return
	}
	log.Printf("écriture de la réponse : %v", err)
	// errors.Is suit les erreurs « enveloppées » avec %w pour retrouver ErrInvalidHeader.
	if errors.Is(err, http1.ErrInvalidHeader) {
		http1.WriteResponse(conn, http1.ErrorResponse(500), now())
	}
}

// lingeringClose ferme proprement après une erreur, quand le client a peut-être encore des octets en route.
// Le problème : si on ferme une socket alors que des octets reçus n'ont pas été lus, le système
// envoie un « reset » (RST) au client, et celui-ci peut perdre notre réponse 400 avant de l'avoir lue.
// La solution (la même que net/http) : fermer seulement notre sens d'écriture (le client voit la fin
// de la réponse), puis lire et jeter ce qu'il envoie encore, pendant 1 s ou 256 Kio au maximum.
func lingeringClose(conn net.Conn) {
	// Assertion de type : CloseWrite n'existe que sur les connexions TCP, pas sur toute net.Conn.
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	// io.LimitReader arrête la lecture après 256 Kio, pour qu'un client ne nous occupe pas indéfiniment.
	io.Copy(io.Discard, io.LimitReader(conn, 256<<10))
}
