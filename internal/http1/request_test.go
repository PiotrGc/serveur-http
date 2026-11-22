// Tests du parser de requêtes HTTP/1.1 (V0.0).
// Chaque cas donne des octets bruts, puis soit la requête attendue,
// soit le code de statut que le serveur devra renvoyer.
// Les règles testées sont listées avec leur source dans « 01 Entrées/Sources HTTP (C).md ».
package http1

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// TestParseRequestValide vérifie les requêtes qui doivent passer.
func TestParseRequestValide(t *testing.T) {
	cas := []struct {
		nom     string
		brut    string
		attendu *Request
	}{
		{
			nom:  "GET simple",
			brut: "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n",
			attendu: &Request{Method: "GET", Target: "/", Proto: "HTTP/1.1",
				Headers: map[string]string{"host": "localhost"}},
		},
		{
			nom:  "casse mixte et espaces autour de la valeur",
			brut: "GET /cours/td1.pdf HTTP/1.1\r\nHOST:   pc  \r\nUser-Agent:\tcurl/8.0\t\r\n\r\n",
			attendu: &Request{Method: "GET", Target: "/cours/td1.pdf", Proto: "HTTP/1.1",
				Headers: map[string]string{"host": "pc", "user-agent": "curl/8.0"}},
		},
		{
			nom:  "en-tête répété fusionné avec une virgule",
			brut: "GET / HTTP/1.1\r\nHost: a\r\nAccept: text/html\r\naccept: */*\r\n\r\n",
			attendu: &Request{Method: "GET", Target: "/", Proto: "HTTP/1.1",
				Headers: map[string]string{"host": "a", "accept": "text/html, */*"}},
		},
		{
			nom:  "valeur vide autorisée",
			brut: "HEAD /x HTTP/1.1\r\nHost: a\r\nX-Vide:\r\n\r\n",
			attendu: &Request{Method: "HEAD", Target: "/x", Proto: "HTTP/1.1",
				Headers: map[string]string{"host": "a", "x-vide": ""}},
		},
		{
			nom:  "HTTP/1.0 sans Host",
			brut: "GET / HTTP/1.0\r\n\r\n",
			attendu: &Request{Method: "GET", Target: "/", Proto: "HTTP/1.0",
				Headers: map[string]string{}},
		},
		{
			nom:  "OPTIONS avec la cible *",
			brut: "OPTIONS * HTTP/1.1\r\nHost: a\r\n\r\n",
			attendu: &Request{Method: "OPTIONS", Target: "*", Proto: "HTTP/1.1",
				Headers: map[string]string{"host": "a"}},
		},
		{
			nom:  "méthode connue mais pas gérée : elle passe le parser (405 plus tard)",
			brut: "POST /upload HTTP/1.1\r\nHost: a\r\n\r\n",
			attendu: &Request{Method: "POST", Target: "/upload", Proto: "HTTP/1.1",
				Headers: map[string]string{"host": "a"}},
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			req, err := ParseRequest(strings.NewReader(c.brut))
			if err != nil {
				t.Fatalf("erreur inattendue : %v", err)
			}
			if !reflect.DeepEqual(req, c.attendu) {
				t.Fatalf("obtenu %+v, attendu %+v", req, c.attendu)
			}
		})
	}
}

// TestParseRequestRefus vérifie que chaque requête invalide donne le bon code de statut.
func TestParseRequestRefus(t *testing.T) {
	longue := strings.Repeat("a", MaxRequestLine)
	cas := []struct {
		nom    string
		brut   string
		statut int
	}{
		// Ligne de requête
		{"ligne sans version", "GET /\r\nHost: a\r\n\r\n", 400},
		{"deux espaces entre les parties", "GET  / HTTP/1.1\r\nHost: a\r\n\r\n", 400},
		{"cible qui ne commence pas par /", "GET cours HTTP/1.1\r\nHost: a\r\n\r\n", 400},
		{"* hors OPTIONS", "GET * HTTP/1.1\r\nHost: a\r\n\r\n", 400},
		{"méthode avec un caractère interdit", "GE(T / HTTP/1.1\r\nHost: a\r\n\r\n", 400},
		{"méthode inconnue", "BREW / HTTP/1.1\r\nHost: a\r\n\r\n", 501},
		{"méthode en minuscules (sensible à la casse)", "get / HTTP/1.1\r\nHost: a\r\n\r\n", 501},
		{"HTTP/2.0", "GET / HTTP/2.0\r\nHost: a\r\n\r\n", 505},
		{"HTTP/0.9", "GET / HTTP/0.9\r\nHost: a\r\n\r\n", 505},
		{"version mal écrite", "GET / HTTP/1\r\nHost: a\r\n\r\n", 400},
		{"version en minuscules", "GET / http/1.1\r\nHost: a\r\n\r\n", 400},
		{"ligne de requête trop longue", "GET /" + longue + " HTTP/1.1\r\nHost: a\r\n\r\n", 414},

		// Fins de ligne
		{"LF seul", "GET / HTTP/1.1\nHost: a\n\n", 400},
		{"CR seul au milieu", "GET /\r HTTP/1.1\r\nHost: a\r\n\r\n", 400},

		// En-têtes
		{"en-tête sans deux-points", "GET / HTTP/1.1\r\nHost a\r\n\r\n", 400},
		{"espace avant les deux-points", "GET / HTTP/1.1\r\nHost : a\r\n\r\n", 400},
		{"nom d'en-tête vide", "GET / HTTP/1.1\r\n: a\r\nHost: a\r\n\r\n", 400},
		{"pliage de ligne (obs-fold)", "GET / HTTP/1.1\r\nHost: a\r\nX: b\r\n c\r\n\r\n", 400},
		{"caractère de contrôle dans la valeur", "GET / HTTP/1.1\r\nHost: a\x00b\r\n\r\n", 400},
		// Sans le contrôle du CR seul, « Host: a » serait lu, « b » avalé, et la requête passerait.
		{"CR seul dans une valeur d'en-tête", "GET / HTTP/1.1\r\nHost: a\rb\r\n\r\n", 400},
		{"HTTP/1.1 sans Host", "GET / HTTP/1.1\r\n\r\n", 400},
		{"deux Host", "GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n", 400},
		{"en-tête trop long", "GET / HTTP/1.1\r\nHost: a\r\nX: " + strings.Repeat("b", MaxHeaderLine) + "\r\n\r\n", 431},
		{"trop d'en-têtes", "GET / HTTP/1.1\r\nHost: a\r\n" + strings.Repeat("X: b\r\n", MaxHeaders) + "\r\n", 431},

		// Fin de flux au milieu d'une requête
		{"coupée après la ligne de requête", "GET / HTTP/1.1\r\nHost: a\r\n", 400},
		{"coupée au milieu d'une ligne", "GET / HT", 400},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			_, err := ParseRequest(strings.NewReader(c.brut))
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("attendu une ParseError %d, obtenu %v", c.statut, err)
			}
			if perr.Status != c.statut {
				t.Fatalf("statut %d, attendu %d (%s)", perr.Status, c.statut, perr.Msg)
			}
		})
	}
}

// TestParseRequestFluxVide : un client qui ferme sans rien envoyer n'est pas une erreur de syntaxe.
// Le serveur devra juste fermer la connexion, sans répondre 400 (utile pour le keep-alive en V0.4).
func TestParseRequestFluxVide(t *testing.T) {
	_, err := ParseRequest(strings.NewReader(""))
	if err != io.EOF {
		t.Fatalf("attendu io.EOF, obtenu %v", err)
	}
}

// TestParseRequestLaisseLeCorps : le parser s'arrête juste après la ligne vide,
// sans consommer le corps. Il faudra le lire ensuite (V0.4).
func TestParseRequestLaisseLeCorps(t *testing.T) {
	r := strings.NewReader("POST / HTTP/1.1\r\nHost: a\r\n\r\ncorps")
	br := NewReader(r)
	if _, err := ParseRequest(br); err != nil {
		t.Fatal(err)
	}
	reste, _ := io.ReadAll(br)
	if string(reste) != "corps" {
		t.Fatalf("reste %q, attendu %q", reste, "corps")
	}
}

// TestParseRequestLimiteExacte : une ligne de requête de exactement MaxRequestLine octets passe.
// Ce test protège contre une erreur d'un octet dans la limite (> au lieu de >=).
func TestParseRequestLimiteExacte(t *testing.T) {
	debut, fin := "GET /", " HTTP/1.1"
	cible := strings.Repeat("a", MaxRequestLine-len(debut)-len(fin))
	brut := debut + cible + fin + "\r\nHost: a\r\n\r\n"
	if _, err := ParseRequest(strings.NewReader(brut)); err != nil {
		t.Fatalf("une ligne de %d octets doit passer : %v", MaxRequestLine, err)
	}
}
