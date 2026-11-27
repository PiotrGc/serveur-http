// Le serveur TCP : il accepte les connexions, lit une requête, appelle le handler et écrit la réponse.
// Utilisé par : cmd/serveur-http (le programme), et les tests d'intégration.
// Ne sait pas ce qu'il sert : c'est le Handler qui décide de la réponse (« Hello » en V0.1,
// les fichiers d'un dossier en V0.2). Le parsing et l'écriture des octets sont dans internal/http1.
//
// Depuis la V0.3 : une goroutine par connexion, des délais de lecture et d'écriture, un nombre
// maximum de connexions simultanées, un arrêt propre (Shutdown) et une ligne de journal par requête.
package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// Valeurs par défaut quand le champ correspondant de Server vaut 0.
const (
	DefaultReadTimeout  = 10 * time.Second // pour recevoir la ligne de requête et tous les en-têtes
	DefaultWriteTimeout = 30 * time.Second // sans aucun octet envoyé (voir deadlineWriter)
	DefaultMaxConns     = 100
)

// Handler reçoit une requête valide et rend la réponse à envoyer.
type Handler func(*http1.Request) *http1.Response

// Server relie une socket d'écoute à un Handler.
// Les champs se règlent avant Serve. Un Server ne sert qu'une fois : après Shutdown, on en crée un autre.
type Server struct {
	Handler Handler
	// Now donne l'heure de l'en-tête Date. nil = time.Now. Les tests y mettent une date fixe.
	Now func() time.Time
	// ReadTimeout : temps maximum pour recevoir la requête complète (ligne + en-têtes).
	// C'est un délai global, pas « entre deux octets » : un client qui envoie un octet de temps
	// en temps (attaque slowloris) est quand même coupé à l'échéance.
	ReadTimeout time.Duration
	// WriteTimeout : temps maximum pendant lequel le client peut ne rien accepter.
	// Il est relancé à chaque morceau envoyé, donc un long téléchargement qui avance n'est pas coupé.
	WriteTimeout time.Duration
	// MaxConns : nombre maximum de connexions traitées en même temps. Les suivantes attendent.
	MaxConns int
	// Logf écrit une ligne de journal. nil = log.Printf (sortie d'erreur, avec la date).
	Logf func(format string, args ...any)

	// État interne, protégé par mu. sync.Mutex : un verrou qu'une seule goroutine peut tenir à la fois.
	// Sans lui, deux goroutines qui modifient la map conns en même temps la corrompraient.
	mu      sync.Mutex
	ln      net.Listener
	conns   map[net.Conn]struct{} // les connexions ouvertes (une map utilisée comme ensemble)
	closing bool                  // Shutdown a commencé
	// wg compte les connexions en cours de traitement, pour que Shutdown sache quand tout est fini.
	wg sync.WaitGroup
}

// Hello est le handler de la V0.1 : il répond « Hello » à toute requête valide.
func Hello(*http1.Request) *http1.Response {
	return &http1.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:    []byte("Hello\n"),
	}
}

// Serve accepte les connexions de ln et traite chacune dans sa propre goroutine,
// jusqu'à ce que ln soit fermé (par Shutdown, ou par l'appelant).
// Rend nil quand ln est fermé normalement, et l'erreur sinon.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	if s.conns == nil {
		s.conns = map[net.Conn]struct{}{}
	}
	s.mu.Unlock()

	// Sémaphore : un channel avec une capacité de MaxConns. Prendre une place = y envoyer une valeur,
	// ce qui bloque quand il est plein. Rendre une place = en retirer une.
	// On prend la place AVANT Accept : quand tout est occupé, les nouveaux clients attendent
	// dans la file d'attente du système, sans qu'on dépense de mémoire pour eux.
	sem := make(chan struct{}, valeur(s.MaxConns, DefaultMaxConns))
	for {
		sem <- struct{}{}
		// Accept attend (bloque) jusqu'à ce qu'un client se connecte.
		conn, err := ln.Accept()
		if err != nil {
			<-sem
			// net.ErrClosed : quelqu'un a fermé ln (arrêt du serveur ou fin d'un test). Ce n'est pas une panne.
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if !s.track(conn) {
			conn.Close() // Shutdown a commencé entre Accept et ici
			<-sem
			return nil
		}
		// go : handleConn tourne en parallèle, et la boucle retourne tout de suite à Accept.
		// C'est ce qui permet de servir 200 clients en même temps.
		go func() {
			defer func() {
				s.untrack(conn)
				<-sem // rend la place
			}()
			s.handleConn(conn)
		}()
	}
}

// track ajoute conn aux connexions suivies. Rend false si Shutdown a déjà commencé.
// wg.Add se fait sous le même verrou que le test de closing : Shutdown ne peut donc pas
// commencer à attendre (wg.Wait) pendant qu'une nouvelle connexion s'ajoute.
func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	return true
}

// untrack retire conn des connexions suivies, une fois son traitement fini.
func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	s.wg.Done()
}

// isClosing dit si Shutdown a commencé.
func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// Shutdown arrête le serveur proprement :
//  1. il n'accepte plus de nouvelles connexions ;
//  2. il coupe les clients qui n'ont pas encore envoyé leur requête (rien à perdre pour eux) ;
//  3. il attend que les réponses en cours se terminent ;
//  4. si ctx expire avant, il ferme toutes les connexions de force et rend ctx.Err().
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	if s.ln != nil {
		s.ln.Close() // Accept rend net.ErrClosed, donc Serve s'arrête
	}
	for c := range s.conns {
		// Une échéance déjà passée réveille tout de suite un Read en attente (avec une erreur de timeout).
		// Une connexion qui envoie déjà sa réponse n'est pas touchée : seule la lecture est concernée.
		c.SetReadDeadline(time.Now())
	}
	s.mu.Unlock()

	// wg.Wait bloque : on l'attend dans une goroutine, pour pouvoir aussi surveiller ctx avec select.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	// select attend le premier des deux événements : tout est fini, ou le délai de ctx est dépassé.
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		return ctx.Err()
	}
}

// handleConn traite une connexion : une requête, une réponse, puis la fermeture.
func (s *Server) handleConn(conn net.Conn) {
	// defer : conn.Close() s'exécute à la sortie de la fonction, quel que soit le chemin pris.
	defer conn.Close()
	// Un panic n'importe où dans le traitement (parser, écriture, Now…) ne doit tuer que cette
	// connexion. Dans une goroutine, un panic non rattrapé arrêterait tout le programme.
	defer func() {
		if r := recover(); r != nil {
			s.logf("panic pendant le traitement de %s : %v", conn.RemoteAddr(), r)
		}
	}()

	// Le délai couvre toute la lecture de la requête : il est fixé une seule fois, avant de lire.
	conn.SetReadDeadline(time.Now().Add(valeur(s.ReadTimeout, DefaultReadTimeout)))
	// Le même lecteur avec tampon doit servir pour toute la connexion (voir http1.NewReader).
	br := http1.NewReader(conn)
	req, err := http1.ParseRequest(br)
	debut := time.Now()
	w := deadlineWriter{conn: conn, timeout: valeur(s.WriteTimeout, DefaultWriteTimeout)}

	var perr *http1.ParseError
	switch {
	case err == io.EOF:
		return // le client s'est connecté puis a fermé sans rien envoyer : rien à répondre
	case errors.Is(err, os.ErrDeadlineExceeded):
		// Le délai de lecture est dépassé. Pendant un arrêt, c'est Shutdown qui l'a provoqué :
		// on ferme sans rien dire. Sinon, le client était trop lent : 408 Request Timeout.
		if s.isClosing() {
			return
		}
		s.write(w, http1.ErrorResponse(408))
		s.logRequest(conn, "-", "-", 408, debut)
		lingeringClose(conn)
		return
	case errors.As(err, &perr):
		s.write(w, http1.ErrorResponse(perr.Status))
		s.logRequest(conn, "-", "-", perr.Status, debut)
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
	s.write(w, resp)
	s.logRequest(conn, req.Method, req.Target, resp.Status, debut)
}

// callHandler appelle le handler et transforme un plantage (panic) en réponse 500.
// Pourquoi ici aussi, en plus du recover de handleConn : le client reçoit une vraie réponse 500
// au lieu d'une connexion coupée sans explication.
func (s *Server) callHandler(req *http1.Request) (resp *http1.Response) {
	// recover() ne marche que dans une fonction lancée par defer. Elle arrête le panic en cours,
	// et on remplace alors la réponse nommée resp par une 500.
	defer func() {
		if r := recover(); r != nil {
			s.logf("panic dans le handler pour %s %s : %v", req.Method, req.Target, r)
			resp = http1.ErrorResponse(500)
		}
	}()
	return s.Handler(req)
}

// write envoie la réponse, puis ferme le fichier qu'elle lisait (s'il y en a un).
// Si un en-tête est invalide, rien n'a encore été envoyé : on peut répondre 500 à la place.
// Pour toute autre erreur (client parti, délai dépassé, fichier tronqué), une partie est peut-être
// déjà partie : on n'écrit plus rien, et la connexion sera fermée par handleConn.
func (s *Server) write(w io.Writer, resp *http1.Response) {
	if resp.File != nil {
		// Fermer le fichier dans tous les cas : sinon chaque requête garderait un descripteur ouvert.
		defer resp.File.Close()
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	err := http1.WriteResponse(w, resp, now())
	if err == nil {
		return
	}
	s.logf("écriture de la réponse : %v", err)
	// errors.Is suit les erreurs « enveloppées » avec %w pour retrouver ErrInvalidHeader.
	if errors.Is(err, http1.ErrInvalidHeader) {
		http1.WriteResponse(w, http1.ErrorResponse(500), now())
	}
}

// deadlineWriter relance le délai d'écriture avant chaque morceau envoyé.
// Pourquoi pas un seul délai pour toute la réponse : un rush de 4 Go sur le Wi-Fi prend plusieurs
// minutes, et serait coupé. Ici, on coupe seulement un client qui n'accepte plus rien pendant
// tout le délai (il ne lit plus, ou il a disparu sans fermer la connexion).
type deadlineWriter struct {
	conn    net.Conn
	timeout time.Duration
}

func (d deadlineWriter) Write(p []byte) (int, error) {
	d.conn.SetWriteDeadline(time.Now().Add(d.timeout))
	return d.conn.Write(p)
}

// logRequest écrit une ligne par requête : IP, méthode, chemin, statut, durée.
// Exemple : « 127.0.0.1 GET /cours/td1.pdf 200 12ms ». Pour une requête refusée par le parser,
// la méthode et le chemin sont « - ».
func (s *Server) logRequest(conn net.Conn, method, target string, status int, debut time.Time) {
	// SplitHostPort sépare « 127.0.0.1:52341 » en « 127.0.0.1 » et « 52341 ».
	ip, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		ip = conn.RemoteAddr().String()
	}
	s.logf("%s %s %s %d %s", ip, method, target, status, time.Since(debut).Round(time.Millisecond))
}

// logf écrit avec s.Logf, ou avec log.Printf si aucun journal n'est fourni.
func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// valeur rend v, ou def si v vaut zéro. Les génériques ([T …]) permettent la même fonction
// pour des durées et des entiers.
func valeur[T time.Duration | int](v, def T) T {
	if v == 0 {
		return def
	}
	return v
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
