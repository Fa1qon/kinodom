package torrents

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"kinodom/internal/httpx"
	"kinodom/internal/player"
)

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
	HandleLocal(pattern, module string, h http.Handler)
}

// Register добавляет маршруты модуля. Открыть раздачу по произвольной ссылке можно только
// с этого ПК: телевизоры открывают раздачи из каталога (этап 7, /releases/{id}/open).
func (s *Service) Register(r Router) {
	r.HandleLocal("POST /api/v1/torrents", s.Name(), http.HandlerFunc(s.handleOpen))
	r.Handle("GET /api/v1/torrents/{hash}", s.Name(), http.HandlerFunc(s.handleStatus))
	r.Handle("POST /api/v1/torrents/{hash}/files/{index}/prepare", s.Name(), http.HandlerFunc(s.handlePrepare))
	r.Handle("GET /api/v1/torrents/{hash}/files/{index}", s.Name(), http.HandlerFunc(s.handleFileStatus))
	r.Handle("GET /stream/{hash}/{index}/{name}", s.Name(), s.StreamHandler())
	r.Handle("GET /m3u/{hash}/{file}", s.Name(), http.HandlerFunc(s.handleM3U))
	r.Handle("POST /api/v1/torrents/{hash}/files/{index}/watch", s.Name(), http.HandlerFunc(s.handleWatch))
	// Удалять скачанное — из домашней сети (спека этапа 7, раздел 10.1).
	r.HandleHome("DELETE /api/v1/downloads/{hash}/{index}", s.Name(), http.HandlerFunc(s.handleDelete))
}

type openRequest struct {
	Magnet  string `json:"magnet,omitempty"`
	Torrent []byte `json:"torrent,omitempty"` // содержимое .torrent; в JSON — base64
}

type openResponse struct {
	Hash string `json:"hash"`
}

type fileStatusResponse struct {
	FileStatus
	StreamURL string `json:"streamUrl"`
}

func (s *Service) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req openRequest
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	ih, err := s.Open(r.Context(), Source{Magnet: req.Magnet, Torrent: req.Torrent})
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, openResponse{Hash: ih.HexString()})
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	ih, ok := parseHash(w, r)
	if !ok {
		return
	}
	st, found := s.Status(ih)
	if !found {
		httpx.WriteError(w, http.StatusNotFound, ErrNotOpen.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handlePrepare(w http.ResponseWriter, r *http.Request) {
	ih, ok := parseHash(w, r)
	if !ok {
		return
	}
	index, ok := parseIndex(w, r)
	if !ok {
		return
	}
	if err := s.Prepare(r.Context(), ih, index); err != nil {
		writePrepareError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, struct{}{})
}

func (s *Service) handleFileStatus(w http.ResponseWriter, r *http.Request) {
	ih, ok := parseHash(w, r)
	if !ok {
		return
	}
	index, ok := parseIndex(w, r)
	if !ok {
		return
	}
	fs, found := s.FileStatus(ih, index)
	if !found {
		httpx.WriteError(w, http.StatusNotFound, "файл не готовится к просмотру: сначала вызовите prepare")
		return
	}
	// Адрес — из Host запроса: клиент уже знает, как достучаться до сервера (спека, раздел 9).
	httpx.WriteJSON(w, http.StatusOK, fileStatusResponse{FileStatus: fs, StreamURL: "http://" + r.Host + fs.StreamPath})
}

// Play — то, что клиент открывает в плеере (основная спека, раздел 13): у всего воспроизводимого
// одинаково.
type Play struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Kind  string `json:"kind"` // video
}

type watchResponse struct {
	Play      Play    `json:"play"`
	M3UURL    string  `json:"m3uUrl"`
	LaunchURL *string `json:"launchUrl"` // только запросу с этого ПК: kinodom:// открывает плеер здесь
}

// handleWatch — «Смотреть» (спека этапа 7, раздел 5.5): файл хранится, фокус очереди — на нём, в
// ответе — чем его открыть. Работает с любого устройства.
func (s *Service) handleWatch(w http.ResponseWriter, r *http.Request) {
	ih, ok := parseHash(w, r)
	if !ok {
		return
	}
	index, ok := parseIndex(w, r)
	if !ok {
		return
	}
	if err := s.Prepare(r.Context(), ih, index); err != nil {
		writePrepareError(w, err)
		return
	}
	name, _ := s.fileName(ih, index)
	title := strings.TrimSuffix(baseName(name), extOf(name))
	path := streamPath(ih, index, name)
	out := watchResponse{Play: Play{URL: "http://" + r.Host + path, Title: title, Kind: "video"},
		M3UURL: fmt.Sprintf("http://%s/m3u/%s/%d.m3u8", r.Host, ih.HexString(), index)}
	if httpx.FromThisPC(r) {
		// Ссылка для kinodom open — строго на 127.0.0.1 и порт API, как бы ни открыли пульт.
		if _, port, err := net.SplitHostPort(r.Host); err == nil {
			l := player.LaunchURL("http://127.0.0.1:"+port+path, title)
			out.LaunchURL = &l
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// writePrepareError — ответ на неудачный выбор файла: такой раздачи или файла нет, списка файлов
// ещё нет, мало места.
func writePrepareError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotOpen), errors.Is(err, ErrNoSuchFile):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNoInfo):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrLowSpace):
		httpx.WriteError(w, http.StatusInsufficientStorage, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}

// handleM3U — плейлист файла для плеера на устройстве без Kinodom (основная спека, раздел 14):
// /m3u/{hash}/{номер}.m3u8. Адрес потока — с хостом из запроса: устройство уже знает, как
// достучаться до сервера.
func (s *Service) handleM3U(w http.ResponseWriter, r *http.Request) {
	ih, ok := parseHash(w, r)
	if !ok {
		return
	}
	num, ok := strings.CutSuffix(r.PathValue("file"), ".m3u8")
	index, err := strconv.Atoi(num)
	if !ok || err != nil || index < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "нужен адрес /m3u/{раздача}/{номер файла}.m3u8")
		return
	}
	name, ok := s.fileName(ih, index)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, ErrNoSuchFile.Error())
		return
	}
	title := strings.TrimSuffix(baseName(name), extOf(name))
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", player.M3UDisposition(title))
	w.Write(player.M3U(title, "http://"+r.Host+streamPath(ih, index, name)))
}

func parseIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	i, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || i < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер файла")
		return 0, false
	}
	return i, true
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	ih, ok := parseHash(w, r)
	if !ok {
		return
	}
	index, ok := parseIndex(w, r)
	if !ok {
		return
	}
	err := s.DeleteFile(r.Context(), ih, index)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotStored):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrWatching), errors.Is(err, errDirMissing):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}
