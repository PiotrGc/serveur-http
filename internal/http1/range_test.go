// Tests de ParseRange (V0.5) : l'en-tête Range sur un fichier de 1000 octets.
package http1

import "testing"

func TestParseRange(t *testing.T) {
	const taille = 1000
	cas := []struct {
		entete     string
		etat       RangeState
		debut, fin int64 // inclus, comme dans Content-Range
	}{
		{"", RangeNone, 0, 0},
		{"bytes=0-99", RangeOK, 0, 99},
		{"bytes=100-", RangeOK, 100, 999},
		{"bytes=-500", RangeOK, 500, 999},     // les 500 derniers octets
		{"bytes=-5000", RangeOK, 0, 999},      // plus que le fichier : tout le fichier
		{"bytes=990-5000", RangeOK, 990, 999}, // fin au-delà : ramenée à la fin du fichier
		{"bytes=999-999", RangeOK, 999, 999},  // le dernier octet seul
		{"bytes=0-0", RangeOK, 0, 0},
		{"bytes=1000-", RangeUnsatisfiable, 0, 0}, // commence après la fin
		{"bytes=5000-6000", RangeUnsatisfiable, 0, 0},
		{"bytes=-0", RangeUnsatisfiable, 0, 0},
		// Mal écrit : on ignore l'en-tête et on envoie tout le fichier (RFC 9110 §14.2).
		{"bytes=abc", RangeNone, 0, 0},
		{"bytes=5-2", RangeNone, 0, 0}, // fin avant le début
		{"bytes=-", RangeNone, 0, 0},
		{"bytes=+5-10", RangeNone, 0, 0},
		{"bytes= 0-99", RangeNone, 0, 0},
		{"items=0-99", RangeNone, 0, 0},                  // unité inconnue
		{"bytes=0-1,5-6", RangeNone, 0, 0},               // plusieurs plages : pas géré, tout le fichier
		{"bytes=99999999999999999999-", RangeNone, 0, 0}, // nombre trop grand
	}
	for _, c := range cas {
		etat, debut, fin := ParseRange(c.entete, taille)
		if etat != c.etat || (etat == RangeOK && (debut != c.debut || fin != c.fin)) {
			t.Errorf("ParseRange(%q) = (%v, %d, %d), attendu (%v, %d, %d)", c.entete, etat, debut, fin, c.etat, c.debut, c.fin)
		}
	}
}

// TestParseRangeFichierVide : aucune plage n'est possible dans un fichier de 0 octet.
func TestParseRangeFichierVide(t *testing.T) {
	for _, h := range []string{"bytes=0-", "bytes=-1", "bytes=0-0"} {
		if etat, _, _ := ParseRange(h, 0); etat != RangeUnsatisfiable {
			t.Errorf("%q sur un fichier vide : %v, attendu RangeUnsatisfiable", h, etat)
		}
	}
}
