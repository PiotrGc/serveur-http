// Le serveur TCP : il accepte les connexions, lit une requête, appelle le handler et écrit la réponse.
// Utilisé par : cmd/serveur-http (le programme), et les tests d'intégration.
// Ne sait pas ce qu'il sert : c'est le Handler qui décide de la réponse (« Hello » en V0.1,
// les fichiers d'un dossier en V0.2). Le parsing et l'écriture des octets sont dans internal/http1.
//
// Depuis la V0.3 : une goroutine par connexion, des délais de lecture et d'écriture, un nombre
// maximum de connexions simultanées, un arrêt propre (Shutdown) et une ligne de journal par requête.
package server

import (
	"bufio"
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
	DefaultIdleTimeout  = 30 * time.Second // attente de la requête suivante sur une connexion keep-alive
	DefaultMaxRequests  = 100              // requêtes par connexion, avant de la fermer
	DefaultMaxPerIP     = 10               // connexions simultanées par IP (un navigateur en ouvre jusqu'à 6)
	DefaultMaxRejects   = 32               // réponses 503 en cours d'envoi au même moment
	DefaultMinRate      = 1 << 10          // 1 Kio/s : débit minimum d'une réponse
	DefaultRateWindow   = 30 * time.Second // fenêtre sur laquelle le débit minimum est mesuré

	// MaxDiscardBody : taille maximale d'un corps de requête qu'on lit pour le sauter (1 Mio).
	// Au-delà, on répond puis on ferme la connexion : lire 10 Go juste pour les jeter serait absurde.
	MaxDiscardBody = 1 << 20
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
	// MaxConns : nombre maximum de connexions traitées en même temps. Au-delà, réponse 503 immédiate.
	MaxConns int
	// MaxConnsPerIP : nombre maximum de connexions simultanées venant d'une même IP. Au-delà, 503.
	// Sans cette limite, une seule machine pourrait prendre toutes les places de MaxConns.
	MaxConnsPerIP int
	// MaxRejects : nombre maximum de réponses 503 en cours d'envoi. Au-delà, fermeture immédiate.
	MaxRejects int
	// MinRate (octets par seconde) et RateWindow : une réponse dont le débit reste sous MinRate
	// pendant toute une fenêtre RateWindow est coupée. C'est la parade au client qui lit 1 octet de
	// temps en temps pour garder sa place indéfiniment (« slow read »).
	MinRate    int64
	RateWindow time.Duration
	// IdleTimeout : combien de temps une connexion keep-alive peut attendre sa requête suivante.
	IdleTimeout time.Duration
	// MaxRequestsPerConn : nombre de requêtes servies sur une connexion avant de la fermer.
	// Ça borne la durée de vie d'une connexion, et donc la place qu'un client garde.
	MaxRequestsPerConn int
	// Logf écrit une ligne de journal. nil = log.Printf (sortie d'erreur, avec la date).
	Logf func(format string, args ...any)

	// État interne, protégé par mu. sync.Mutex : un verrou qu'une seule goroutine peut tenir à la fois.
	// Sans lui, deux goroutines qui modifient la map conns en même temps la corrompraient.
	mu      sync.Mutex
	ln      net.Listener
	conns   map[net.Conn]struct{} // les connexions ouvertes (une map utilisée comme ensemble)
	perIP   map[string]int        // nombre de connexions servies par IP
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
		s.perIP = map[string]int{}
	}
	s.mu.Unlock()

	// Sémaphore : un channel avec une capacité de MaxConns. Prendre une place = y envoyer une valeur,
	// rendre une place = en retirer une.
	sem := make(chan struct{}, valeur(s.MaxConns, DefaultMaxConns))
	// Les refus (503) ont leur propre limite : sans elle, un flot de connexions lancerait autant de
	// goroutines de refus (chacune vit jusqu'à 2 s), et les sockets du PC s'épuiseraient.
	rejectSem := make(chan struct{}, valeur(s.MaxRejects, DefaultMaxRejects))
	var backoff time.Duration
	for {
		// Accept attend (bloque) jusqu'à ce qu'un client se connecte.
		conn, err := ln.Accept()
		if err != nil {
			// net.ErrClosed : quelqu'un a fermé ln (arrêt du serveur ou fin d'un test). Ce n'est pas une panne.
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Autre erreur (ex. plus de descripteurs de fichiers libres, « EMFILE ») : souvent passagère.
			// On attend un peu, de plus en plus longtemps (5 ms, 10 ms… jusqu'à 1 s), puis on réessaie,
			// au lieu d'arrêter tout le serveur. C'est ce que fait net/http.
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			s.logf("Accept : %v ; nouvel essai dans %v", err, backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		ip := hostOf(conn)

		// select avec default : on essaie de prendre une place SANS attendre. Si tout est plein, on
		// refuse ce client tout de suite (503) au lieu de bloquer Accept : depuis la V0.7, des clients
		// lents ne peuvent plus bloquer la file d'attente de tout le monde.
		full := false
		select {
		case sem <- struct{}{}:
		default:
			full = true
		}
		switch s.track(conn, ip, full) {
		case trackClosing:
			conn.Close() // Shutdown a commencé entre Accept et ici
			if !full {
				<-sem
			}
			return nil
		case trackRejected:
			if !full {
				<-sem // la place prise n'est pas utilisée : la limite par IP est atteinte
			}
			select {
			case rejectSem <- struct{}{}:
				go func() {
					defer func() {
						s.untrack(conn, ip, false)
						<-rejectSem
					}()
					s.reject(conn, ip)
				}()
			default:
				// Trop de refus déjà en cours : on ferme tout de suite, sans répondre. C'est brutal,
				// mais ça ne coûte rien, et un client normal ne tombe pas dans ce cas.
				conn.Close()
				s.untrack(conn, ip, false)
			}
		case trackOK:
			// go : handleConn tourne en parallèle, et la boucle retourne tout de suite à Accept.
			// C'est ce qui permet de servir 200 clients en même temps.
			go func() {
				defer func() {
					s.untrack(conn, ip, true)
					<-sem // rend la place
				}()
				s.handleConn(conn, ip)
			}()
		}
	}
}

// Résultats possibles de track.
const (
	trackOK       = iota // la connexion est servie
	trackRejected        // serveur plein ou limite par IP atteinte : réponse 503
	trackClosing         // Shutdown a commencé : on ferme sans rien dire
)

// track ajoute conn aux connexions suivies (aussi celles qu'on refuse, pour que Shutdown les attende)
// et décide si elle est servie. full : il ne restait aucune place globale.
// wg.Add se fait sous le même verrou que le test de closing : Shutdown ne peut donc pas
// commencer à attendre (wg.Wait) pendant qu'une nouvelle connexion s'ajoute.
func (s *Server) track(conn net.Conn, ip string, full bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return trackClosing
	}
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	if full || s.perIP[ip] >= valeur(s.MaxConnsPerIP, DefaultMaxPerIP) {
		return trackRejected
	}
	s.perIP[ip]++
	return trackOK
}

// untrack retire conn des connexions suivies. counted : elle comptait dans la limite de son IP.
func (s *Server) untrack(conn net.Conn, ip string, counted bool) {
	s.mu.Lock()
	delete(s.conns, conn)
	if counted {
		s.perIP[ip]--
		if s.perIP[ip] == 0 {
			delete(s.perIP, ip) // pas d'entrée qui traîne pour une IP qui n'a plus de connexion
		}
	}
	s.mu.Unlock()
	s.wg.Done()
}

// reject répond 503 tout de suite, puis ferme. Retry-After invite le client à réessayer dans 1 s.
// Délai d'écriture court : un client qu'on refuse ne doit pas pouvoir nous occuper longtemps.
func (s *Server) reject(conn net.Conn, ip string) {
	defer conn.Close()
	resp := http1.ErrorResponse(503)
	resp.Headers["Retry-After"] = "1"
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	http1.WriteResponse(conn, resp, s.now())
	s.logf("%s - - 503 (trop de connexions)", ip)
	lingeringClose(conn) // sa requête est peut-être déjà arrivée : sans ça, le reset ferait perdre la 503
}

// hostOf rend l'IP du client, sans le port (« 192.168.1.30:52341 » → « 192.168.1.30 »).
func hostOf(conn net.Conn) string {
	ip, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return ip
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

// handleConn traite une connexion : une requête après l'autre (keep-alive), jusqu'à ce que
// le client ou le serveur décide de fermer.
func (s *Server) handleConn(conn net.Conn, ip string) {
	// defer : conn.Close() s'exécute à la sortie de la fonction, quel que soit le chemin pris.
	defer conn.Close()
	// Un panic n'importe où dans le traitement (parser, écriture, Now…) ne doit tuer que cette
	// connexion. Dans une goroutine, un panic non rattrapé arrêterait tout le programme.
	defer func() {
		if r := recover(); r != nil {
			s.logf("panic pendant le traitement de %s : %v", conn.RemoteAddr(), r)
		}
	}()

	// Le même lecteur avec tampon sert pour toute la connexion : il peut déjà contenir le début
	// de la requête suivante (pipelining), qu'un nouveau lecteur perdrait.
	br := http1.NewReader(conn)
	max := valeur(s.MaxRequestsPerConn, DefaultMaxRequests)

	for n := 1; ; n++ {
		if n > 1 {
			// Entre deux requêtes : on attend le premier octet de la suivante, au plus IdleTimeout.
			// Peek(1) regarde cet octet sans le consommer : le parser le lira ensuite normalement.
			conn.SetReadDeadline(time.Now().Add(valeur(s.IdleTimeout, DefaultIdleTimeout)))
			// Ordre important : on pose le délai PUIS on regarde closing. Si Shutdown passe juste avant,
			// on voit closing ; s'il passe juste après, son délai « maintenant » remplace le nôtre.
			if s.isClosing() {
				return
			}
			if _, err := br.Peek(1); err != nil {
				return // client parti, inactif trop longtemps, ou arrêt : fermeture sans rien envoyer
			}
		}
		// Le délai couvre toute la lecture de la requête : il est fixé une seule fois, avant de lire.
		conn.SetReadDeadline(time.Now().Add(valeur(s.ReadTimeout, DefaultReadTimeout)))
		if s.isClosing() && n > 1 {
			return
		}
		if !s.serveOne(conn, ip, br, n >= max) {
			return
		}
	}
}

// serveOne lit une requête, la traite et écrit la réponse.
// last : c'est la dernière requête autorisée sur cette connexion (MaxRequestsPerConn).
// Rend true si la connexion doit rester ouverte pour une requête suivante.
func (s *Server) serveOne(conn net.Conn, ip string, br *bufio.Reader, last bool) bool {
	req, err := http1.ParseRequest(br)
	debut := time.Now()
	// Un writer neuf par réponse : la mesure du débit minimum recommence à chaque réponse
	// (le temps passé à attendre la requête suivante ne doit pas compter comme de la lenteur).
	w := &deadlineWriter{
		conn: conn, timeout: valeur(s.WriteTimeout, DefaultWriteTimeout),
		minRate: valeur(s.MinRate, DefaultMinRate), window: valeur(s.RateWindow, DefaultRateWindow),
	}

	var perr *http1.ParseError
	switch {
	case err == io.EOF:
		return false // le client a fermé sans envoyer de requête : rien à répondre
	case errors.Is(err, os.ErrDeadlineExceeded):
		// Le délai de lecture est dépassé. Pendant un arrêt, c'est Shutdown qui l'a provoqué :
		// on ferme sans rien dire. Sinon, le client était trop lent : 408 Request Timeout.
		if s.isClosing() {
			return false
		}
		s.refuse(conn, w, 408, debut)
		return false
	case errors.As(err, &perr):
		s.refuse(conn, w, perr.Status, debut)
		return false
	case err != nil:
		return false // erreur réseau (connexion coupée) : impossible de répondre
	}

	// Le corps de la requête : on ne l'utilise pas (lecture seule), mais il faut le lire pour
	// le sauter, sinon ses octets seraient pris pour la requête suivante.
	bodyLen, err := http1.BodyLength(req)
	if errors.As(err, &perr) {
		s.refuse(conn, w, perr.Status, debut)
		return false
	}
	keep := http1.WantsKeepAlive(req) && !last
	tooBig := bodyLen > MaxDiscardBody
	if tooBig {
		keep = false // on ne lit pas un énorme corps juste pour le jeter : on fermera après la réponse
	} else if bodyLen > 0 {
		// io.CopyN vers io.Discard : lire exactement bodyLen octets et les jeter.
		if _, err := io.CopyN(io.Discard, br, bodyLen); err != nil {
			return false // corps incomplet (client parti ou trop lent) : on ne peut plus rien lire de sûr
		}
	}

	req.RemoteAddr = ip // le handler (et l'authentification) en a besoin pour savoir qui demande
	resp := s.callHandler(req)
	// RFC 9110 §9.3.2 : HEAD rend exactement les en-têtes de GET, sans le corps.
	// C'est géré ici une fois pour toutes, plutôt que dans chaque handler.
	if req.Method == "HEAD" {
		resp.OmitBody = true
	}
	// Pendant un arrêt, on prévient le client que c'est la dernière réponse.
	resp.KeepAlive = keep && !s.isClosing()
	ok := s.write(w, resp)
	s.logRequest(conn, req.Method, req.Target, resp.Status, debut)
	if tooBig {
		lingeringClose(conn) // le corps non lu provoquerait sinon un reset qui ferait perdre la réponse
	}
	return ok && resp.KeepAlive
}

// refuse répond avec un code d'erreur, puis ferme en douceur (lingeringClose).
// Après une requête mal formée, on ne sait plus où commence la suivante : la connexion est perdue.
func (s *Server) refuse(conn net.Conn, w io.Writer, status int, debut time.Time) {
	s.write(w, http1.ErrorResponse(status))
	s.logRequest(conn, "-", "-", status, debut)
	lingeringClose(conn)
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
// Rend true si la réponse prévue est partie en entier (la connexion peut alors resservir).
func (s *Server) write(w io.Writer, resp *http1.Response) bool {
	if resp.File != nil {
		// Fermer le fichier dans tous les cas : sinon chaque requête garderait un descripteur ouvert.
		defer resp.File.Close()
	}
	err := http1.WriteResponse(w, resp, s.now())
	if err == nil {
		return true
	}
	s.logf("écriture de la réponse : %v", err)
	// errors.Is suit les erreurs « enveloppées » avec %w pour retrouver ErrInvalidHeader.
	// La 500 de remplacement annonce « Connection: close » (KeepAlive vaut false).
	if errors.Is(err, http1.ErrInvalidHeader) {
		http1.WriteResponse(w, http1.ErrorResponse(500), s.now())
	}
	return false
}

// now : l'heure de l'en-tête Date (s.Now dans les tests, sinon l'heure réelle).
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// errTooSlow : le client lit plus lentement que le débit minimum.
var errTooSlow = errors.New("client trop lent : débit sous le minimum")

// deadlineWriter protège l'envoi d'une réponse contre deux sortes de clients :
//   - celui qui n'accepte plus rien : le délai d'écriture est relancé avant chaque morceau, donc on
//     ne coupe qu'un client bloqué pendant tout le délai (un long téléchargement qui avance passe) ;
//   - celui qui lit très lentement pour garder sa place (« slow read ») : on mesure les octets envoyés
//     sur des fenêtres de durée window, et on coupe si une fenêtre complète reste sous minRate.
//
// Un deadlineWriter sert pour UNE réponse (voir serveOne) : il garde un état, d'où le pointeur.
type deadlineWriter struct {
	conn    net.Conn
	timeout time.Duration
	minRate int64         // octets par seconde
	window  time.Duration // durée d'une fenêtre de mesure

	windowStart time.Time // début de la fenêtre en cours (zéro avant la première écriture)
	windowBytes int64     // octets envoyés depuis windowStart
}

// Write envoie p. Le débit est contrôlé au début de chaque morceau (4 Kio au plus, à cause du
// tampon de WriteResponse). Un seul morceau ne peut pas rester bloqué indéfiniment non plus :
// l'échéance d'écriture le coupe s'il dépasse timeout. Les deux ensemble couvrent tous les rythmes lents.
func (d *deadlineWriter) Write(p []byte) (int, error) {
	now := time.Now()
	if d.windowStart.IsZero() {
		d.windowStart = now
	} else if elapsed := now.Sub(d.windowStart); elapsed >= d.window {
		// Fin d'une fenêtre : a-t-on envoyé au moins minRate octets par seconde ?
		if float64(d.windowBytes) < float64(d.minRate)*elapsed.Seconds() {
			return 0, errTooSlow
		}
		d.windowStart, d.windowBytes = now, 0
	}
	d.conn.SetWriteDeadline(now.Add(d.timeout))
	n, err := d.conn.Write(p)
	d.windowBytes += int64(n)
	return n, err
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
func valeur[T time.Duration | int | int64](v, def T) T {
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
