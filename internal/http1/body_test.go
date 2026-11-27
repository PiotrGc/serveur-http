// Tests de BodyLength et WantsKeepAlive (V0.4).
package http1

import (
	"errors"
	"testing"
)

func req(proto string, headers map[string]string) *Request {
	return &Request{Method: "POST", Target: "/", Proto: proto, Headers: headers}
}

// TestBodyLength : la taille du corps annoncée, ou le refus avec le bon code.
func TestBodyLength(t *testing.T) {
	cas := []struct {
		nom     string
		headers map[string]string
		taille  int64
		statut  int // 0 = pas d'erreur
	}{
		{"sans corps", map[string]string{}, 0, 0},
		{"Content-Length 5", map[string]string{"content-length": "5"}, 5, 0},
		{"Content-Length 0", map[string]string{"content-length": "0"}, 0, 0},
		{"pas un nombre", map[string]string{"content-length": "abc"}, 0, 400},
		{"signe +", map[string]string{"content-length": "+5"}, 0, 400},
		{"négatif", map[string]string{"content-length": "-1"}, 0, 400},
		{"vide", map[string]string{"content-length": ""}, 0, 400},
		{"deux valeurs fusionnées", map[string]string{"content-length": "5, 6"}, 0, 400},
		{"trop de chiffres", map[string]string{"content-length": "12345678901234567890"}, 0, 400},
		{"Transfer-Encoding non géré", map[string]string{"transfer-encoding": "chunked"}, 0, 501},
		{"Content-Length ET Transfer-Encoding", map[string]string{"content-length": "5", "transfer-encoding": "chunked"}, 0, 400},
	}
	for _, c := range cas {
		n, err := BodyLength(req("HTTP/1.1", c.headers))
		var perr *ParseError
		switch {
		case c.statut == 0 && (err != nil || n != c.taille):
			t.Errorf("%s : obtenu (%d, %v), attendu (%d, nil)", c.nom, n, err, c.taille)
		case c.statut != 0 && (!errors.As(err, &perr) || perr.Status != c.statut):
			t.Errorf("%s : obtenu %v, attendu le statut %d", c.nom, err, c.statut)
		}
	}
}

// TestWantsKeepAlive : HTTP/1.1 garde la connexion par défaut, HTTP/1.0 seulement sur demande.
func TestWantsKeepAlive(t *testing.T) {
	cas := []struct {
		proto, connection string
		attendu           bool
	}{
		{"HTTP/1.1", "", true},
		{"HTTP/1.1", "close", false},
		{"HTTP/1.1", "Close", false}, // insensible à la casse
		{"HTTP/1.1", "keep-alive, close", false},
		{"HTTP/1.0", "", false},
		{"HTTP/1.0", "keep-alive", true},
		{"HTTP/1.0", "Keep-Alive", true},
		{"HTTP/1.0", "keep-alive, close", false},
	}
	for _, c := range cas {
		h := map[string]string{}
		if c.connection != "" {
			h["connection"] = c.connection
		}
		if got := WantsKeepAlive(req(c.proto, h)); got != c.attendu {
			t.Errorf("%s Connection: %q → %v, attendu %v", c.proto, c.connection, got, c.attendu)
		}
	}
}
