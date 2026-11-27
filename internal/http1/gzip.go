// Compression gzip et encodage « chunked » des réponses.
// Utilisé par : WriteResponse (response.go) quand Response.Compress est vrai, et par internal/files
// pour savoir si le client accepte gzip.
// Pourquoi les deux vont ensemble : la taille compressée n'est connue qu'à la fin de la compression.
// On ne peut donc pas envoyer Content-Length avant le corps : on envoie le corps en morceaux
// (« chunks ») qui annoncent chacun leur taille, puis un morceau vide pour dire « c'est fini ».
package http1

import (
	"io"
	"strconv"
	"strings"
)

// AcceptsGzip dit si le client accepte une réponse compressée en gzip (RFC 9110 §12.5.3).
// L'en-tête est une liste avec des poids : « gzip;q=0.5, br ». Un poids q=0 veut dire « jamais ».
// « * » veut dire « n'importe quel encodage », sauf ceux refusés explicitement.
func AcceptsGzip(header string) bool {
	gzipQ, starQ := -1.0, -1.0 // -1 = pas mentionné
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		q := 1.0 // poids par défaut
		if k, v, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			var err error
			if q, err = strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
				q = 0 // poids illisible : prudence, on considère « refusé »
			}
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "gzip":
			gzipQ = q
		case "*":
			starQ = q
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0 // gzip est cité : c'est son poids qui décide, même si « * » est là
	}
	return starQ > 0
}

// chunkedWriter écrit ce qu'on lui donne au format chunked (RFC 9112 §7.1) :
//
//	<taille en hexadécimal>\r\n<données>\r\n   … autant de fois que nécessaire, puis
//	0\r\n\r\n                                   le morceau vide final
//
// Le morceau final n'est pas écrit ici : c'est l'appelant qui l'ajoute à la fin (voir WriteResponse).
type chunkedWriter struct{ w io.Writer }

func (c chunkedWriter) Write(p []byte) (int, error) {
	// Un morceau de taille 0 voudrait dire « fin du corps » : on n'en écrit jamais au milieu.
	if len(p) == 0 {
		return 0, nil
	}
	// strconv.FormatInt(…, 16) : la taille en hexadécimal, ex. 4096 → « 1000 ».
	if _, err := io.WriteString(c.w, strconv.FormatInt(int64(len(p)), 16)+"\r\n"); err != nil {
		return 0, err
	}
	if _, err := c.w.Write(p); err != nil {
		return 0, err
	}
	if _, err := io.WriteString(c.w, "\r\n"); err != nil {
		return 0, err
	}
	return len(p), nil
}
