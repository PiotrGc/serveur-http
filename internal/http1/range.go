// Lecture de l'en-tête Range : quelle partie d'un fichier le client demande.
// Utilisé par : internal/files, pour envoyer un morceau d'un fichier (206 Partial Content).
// C'est ce qui permet d'avancer dans une vidéo, ou de reprendre un téléchargement coupé.
// Ne lit pas le fichier : il traduit seulement « bytes=100-199 » en positions.
package http1

import (
	"strconv"
	"strings"
)

// RangeState : le résultat de ParseRange.
type RangeState int

const (
	RangeNone          RangeState = iota // pas de Range, ou Range ignoré : on envoie tout le fichier (200)
	RangeOK                              // une plage valide : on envoie ce morceau (206)
	RangeUnsatisfiable                   // la plage est hors du fichier : 416
)

// ParseRange lit un en-tête Range pour un fichier de size octets (RFC 9110 §14.1.2).
// Formes acceptées, une seule plage :
//
//	bytes=0-99     les octets 0 à 99 (inclus)
//	bytes=100-     de l'octet 100 jusqu'à la fin
//	bytes=-500     les 500 derniers octets
//
// start et end sont inclus, comme dans l'en-tête Content-Range.
// Un en-tête mal écrit ou à plusieurs plages est ignoré (RangeNone) : la RFC 9110 §14.2 autorise
// le serveur à ignorer Range, et envoyer tout le fichier reste une réponse correcte.
func ParseRange(header string, size int64) (state RangeState, start, end int64) {
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return RangeNone, 0, 0
	}
	// digits refuse tout ce qui n'est pas un chiffre : « 0-1,5-6 » (plusieurs plages) est donc
	// ignoré ici aussi, car « 1,5-6 » n'est pas un nombre.
	first, last, ok := strings.Cut(spec, "-")
	if !ok || (first == "" && last == "") || !digits(first) || !digits(last) {
		return RangeNone, 0, 0
	}

	if first == "" {
		// « -500 » : les n derniers octets.
		n, _ := strconv.ParseInt(last, 10, 64)
		if n == 0 || size == 0 {
			return RangeUnsatisfiable, 0, 0
		}
		n = min(n, size) // plus que le fichier : tout le fichier
		return RangeOK, size - n, size - 1
	}

	start, _ = strconv.ParseInt(first, 10, 64)
	end = size - 1
	if last != "" {
		end, _ = strconv.ParseInt(last, 10, 64)
		if end < start {
			return RangeNone, 0, 0 // « 5-2 » : mal écrit
		}
	}
	if start >= size {
		return RangeUnsatisfiable, 0, 0 // commence après la fin du fichier
	}
	return RangeOK, start, min(end, size-1) // une fin au-delà du fichier est ramenée à la fin
}

// digits dit si s ne contient que des chiffres, et au plus 18 (assez pour 999 Po,
// et toujours sans dépassement d'int64). La chaîne vide est acceptée : elle veut dire « absent ».
func digits(s string) bool {
	return len(s) <= 18 && strings.Trim(s, "0123456789") == ""
}
