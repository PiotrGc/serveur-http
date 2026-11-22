// Erreurs du parser HTTP/1.1.
// Utilisé par : request.go, qui les crée, et plus tard le serveur, qui lit Status
// pour savoir quel code renvoyer au client (400, 414, 431, 501, 505).
// Ne s'occupe pas d'écrire la réponse : il ne fait que transporter le code et la raison.
package http1

import "fmt"

// ParseError décrit une requête refusée.
// Status est le code HTTP que le serveur devra répondre, Msg explique pourquoi (pour les logs).
type ParseError struct {
	Status int
	Msg    string
}

// Error rend l'erreur lisible, par exemple « 400 : ligne de requête mal formée ».
// Avec cette méthode, *ParseError satisfait l'interface error de Go, donc on peut le
// renvoyer partout où une error est attendue, et le retrouver avec errors.As.
func (e *ParseError) Error() string {
	return fmt.Sprintf("%d : %s", e.Status, e.Msg)
}

// Raccourcis pour créer les erreurs du parser, un par code de statut.
// Sources des codes : « 01 Entrées/Sources HTTP (C).md ».
func errBadRequest(msg string) error          { return &ParseError{400, msg} } // syntaxe invalide
func errURITooLong(msg string) error          { return &ParseError{414, msg} } // ligne de requête trop longue
func errHeadersTooLarge(msg string) error     { return &ParseError{431, msg} } // en-tête(s) trop gros
func errNotImplemented(msg string) error      { return &ParseError{501, msg} } // méthode inconnue
func errVersionNotSupported(msg string) error { return &ParseError{505, msg} } // version non gérée
