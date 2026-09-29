package torrents

import (
	"errors"
	"net/http"
	"strconv"

	"kinodom/internal/httpx"
)

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
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
	// Удалять скачанное — только с этого ПК (спека, раздел 13).
	r.HandleLocal("DELETE /api/v1/downloads/{hash}/{index}", s.Name(), http.HandlerFunc(s.handleDelete))
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
	err := s.Prepare(r.Context(), ih, index)
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusAccepted, struct{}{})
	case errors.Is(err, ErrNotOpen), errors.Is(err, ErrNoSuchFile):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNoInfo):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
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
