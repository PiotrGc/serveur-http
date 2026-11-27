// Écriture des réponses HTTP/1.1 : ligne de statut, en-têtes, ligne vide, corps.
// Utilisé par : internal/server, pour répondre à chaque requête (y compris les erreurs du parser).
// Le corps part soit avec un Content-Length, soit compressé en gzip et découpé en chunked (V0.6).
package http1

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ServerName est la valeur de l'en-tête Server envoyé dans chaque réponse.
const ServerName = "serveur-http"

// TimeFormat : le format des dates HTTP (RFC 9110 §5.6.7, « IMF-fixdate »), toujours en GMT.
// En Go, on décrit un format avec la date de référence « Mon Jan 2 15:04:05 2006 ».
// Il sert pour Date, Last-Modified, If-Modified-Since et If-Range.
const TimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

// ErrInvalidHeader : WriteResponse a refusé un en-tête, et rien n'a été écrit.
var ErrInvalidHeader = errors.New("en-tête de réponse invalide")

// Response est une réponse à envoyer. Headers contient seulement les en-têtes propres à la réponse
// (ex. Content-Type) : Content-Length, Date, Server et Connection sont ajoutés par WriteResponse.
// Le corps est soit Body (petit, déjà en mémoire : une page d'erreur, un listing),
// soit File (lu en continu, pour ne jamais charger un fichier de plusieurs Go en mémoire).
type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
	// File : le corps lu morceau par morceau. Le serveur le ferme après l'envoi (voir server.write).
	// io.ReadCloser = tout ce qui sait lire (Read) et se fermer (Close), comme un *os.File.
	File io.ReadCloser
	Size int64 // nombre d'octets de File à envoyer ; ignoré si File vaut nil
	// OmitBody : pour HEAD, on envoie les mêmes en-têtes (Content-Length compris) mais pas le corps.
	OmitBody bool
	// KeepAlive : le serveur garde la connexion ouverte après cette réponse (« Connection: keep-alive »).
	// false par défaut : « Connection: close ». C'est le serveur qui le décide, pas le handler.
	KeepAlive bool
	// Compress : le corps part compressé en gzip, en chunked (pas de Content-Length).
	// Le handler ne le met que si le client accepte gzip ET parle HTTP/1.1 (chunked n'existe pas en 1.0).
	Compress bool
}

// statusText : le texte standard de chaque code utilisé (RFC 9110 §15, RFC 6585 §5 pour 431).
var statusText = map[int]string{
	200: "OK",
	206: "Partial Content",
	301: "Moved Permanently",
	304: "Not Modified",
	400: "Bad Request",
	401: "Unauthorized",
	403: "Forbidden",
	404: "Not Found",
	405: "Method Not Allowed",
	408: "Request Timeout",
	414: "URI Too Long",
	416: "Range Not Satisfiable",
	429: "Too Many Requests",
	431: "Request Header Fields Too Large",
	500: "Internal Server Error",
	501: "Not Implemented",
	503: "Service Unavailable",
	505: "HTTP Version Not Supported",
}

// StatusText rend le texte d'un code (« Not Found » pour 404), ou "" si le code est inconnu.
func StatusText(code int) string {
	return statusText[code]
}

// autoHeaders : les en-têtes que WriteResponse calcule lui-même. S'ils sont dans resp.Headers,
// ils sont ignorés : un Content-Length faux ferait lire au client trop ou trop peu d'octets.
// Transfer-Encoding aussi : c'est lui qui dit où finit le corps, il ne doit venir que de nous.
var autoHeaders = map[string]bool{"content-length": true, "date": true, "server": true, "connection": true, "transfer-encoding": true}

// WriteResponse écrit resp en entier dans w.
// now sert à l'en-tête Date. On le passe en paramètre au lieu d'appeler time.Now() ici,
// pour que les tests puissent vérifier les octets exacts avec une date fixe.
// Rend une erreur sans rien écrire si un en-tête est invalide, et l'erreur de w si l'écriture échoue.
func WriteResponse(w io.Writer, resp *Response, now time.Time) error {
	length := int64(len(resp.Body))
	if resp.File != nil {
		length = resp.Size
	}
	headers := map[string]string{
		"Content-Length": strconv.FormatInt(length, 10),
		"Date":           now.UTC().Format(TimeFormat),
		"Server":         ServerName,
		// RFC 9112 §9.6 : on annonce si on ferme après cette réponse. « keep-alive » est implicite en
		// HTTP/1.1, mais l'écrire est sans risque et indispensable pour un client HTTP/1.0.
		"Connection": "close",
	}
	if resp.KeepAlive {
		headers["Connection"] = "keep-alive"
	}
	// RFC 9110 §15.4.5 : une 304 n'a jamais de corps. On n'envoie pas non plus Content-Length,
	// qui dirait « le fichier fait 0 octet » alors qu'il n'a simplement pas changé.
	noBody := resp.Status == 304
	if noBody {
		delete(headers, "Content-Length")
	}
	// gzip : la taille compressée n'est connue qu'à la fin, donc pas de Content-Length mais du
	// chunked, qui marque lui-même la fin du corps (RFC 9112 §6.1). Les deux ensemble sont interdits.
	compress := resp.Compress && !noBody
	if compress {
		delete(headers, "Content-Length")
		headers["Content-Encoding"] = "gzip"
		headers["Transfer-Encoding"] = "chunked"
	}
	for name, value := range resp.Headers {
		if autoHeaders[strings.ToLower(name)] {
			continue
		}
		// Sécurité : un CR ou un LF dans un nom ou une valeur permettrait d'écrire de faux en-têtes
		// ou un faux corps (« response splitting »). On vérifie tout avant d'écrire le moindre octet.
		if !isToken(name) || !isValidValue(value) {
			// %w « enveloppe » ErrInvalidHeader : l'appelant le retrouve avec errors.Is.
			return fmt.Errorf("%w : %s", ErrInvalidHeader, name)
		}
		headers[name] = value
	}

	// Tri des noms : l'ordre d'une map Go change à chaque exécution. Trier rend la sortie stable,
	// donc testable octet par octet.
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	// bufio.Writer regroupe les petites écritures en un seul envoi sur la socket.
	// Flush envoie ce qui reste dans le tampon : sans lui, rien ne partirait.
	bw := bufio.NewWriter(w)
	text := StatusText(resp.Status)
	if text == "" {
		text = "Unknown"
	}
	bw.WriteString("HTTP/1.1 " + strconv.Itoa(resp.Status) + " " + text + "\r\n")
	for _, name := range names {
		bw.WriteString(name + ": " + headers[name] + "\r\n")
	}
	bw.WriteString("\r\n")
	switch {
	case resp.OmitBody || noBody:
		// HEAD ou 304 : rien après la ligne vide.
	case compress:
		if err := writeGzipChunked(bw, resp); err != nil {
			return err
		}
	case resp.File != nil:
		// io.CopyN copie exactement Size octets par morceaux (32 Kio à la fois), sans tout charger.
		// Si le fichier a moins d'octets que prévu, CopyN rend io.EOF : on remonte l'erreur pour que
		// le serveur coupe la connexion, plutôt que de laisser le client attendre des octets qui ne viendront pas.
		if _, err := io.CopyN(bw, resp.File, resp.Size); err != nil {
			return err
		}
	default:
		bw.Write(resp.Body)
	}
	// Les erreurs des WriteString précédents sont gardées par le bufio.Writer et rendues ici.
	return bw.Flush()
}

// writeGzipChunked écrit le corps compressé, en chunked : les octets passent par trois étapes,
// source → gzip (compression) → chunkedWriter (découpage) → bw (tampon de la socket).
// Rien n'est chargé en entier : un fichier texte de 1 Go se compresse au fil de l'eau.
func writeGzipChunked(bw *bufio.Writer, resp *Response) error {
	zw := gzip.NewWriter(chunkedWriter{w: bw})
	var err error
	if resp.File != nil {
		_, err = io.CopyN(zw, resp.File, resp.Size)
	} else {
		_, err = zw.Write(resp.Body)
	}
	if err != nil {
		return err
	}
	// Close vide le tampon de gzip et écrit sa fin (somme de contrôle CRC-32 et taille d'origine).
	// Sans lui, le client recevrait un gzip tronqué.
	if err := zw.Close(); err != nil {
		return err
	}
	// Le morceau vide final : « 0 », ligne vide. Il dit au client que le corps est fini, et donc
	// où commence la réponse suivante sur une connexion keep-alive.
	_, err = bw.WriteString("0\r\n\r\n")
	return err
}

// ErrorResponse fabrique une réponse d'erreur en texte brut, par ex. « 404 Not Found ».
func ErrorResponse(status int) *Response {
	return &Response{
		Status:  status,
		Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:    []byte(strconv.Itoa(status) + " " + StatusText(status) + "\n"),
	}
}
