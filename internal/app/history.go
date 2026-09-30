package app

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/catalog"
	"kinodom/internal/history"
	"kinodom/internal/httpx"
	"kinodom/internal/library"
)

// initHistory — история просмотров по устройствам (спека этапа 8, раздел 7): место по потоку
// торрентов, маршруты для пульта и своего плеера.
func (a *App) initHistory() {
	a.History = history.New(a.DB)
	a.API.Handle("GET /api/v1/history", "", http.HandlerFunc(a.handleHistory))
	a.API.Handle("GET /api/v1/history/{hash}", "", http.HandlerFunc(a.handleHistoryFiles))
	a.API.HandleHome("PUT /api/v1/history/{hash}/{index}", "", http.HandlerFunc(a.handleHistoryPut))
	a.API.HandleHome("DELETE /api/v1/history/{hash}", "", http.HandlerFunc(a.handleHistoryDelete))
}

// historyItem — раздача в истории устройства: раздача каталога или карточка медиатеки и последний
// файл с именем.
type historyItem struct {
	history.Item
	Release *catalog.ReleaseRef  `json:"release"` // null — раздачи нет в каталоге
	Library *library.CardSummary `json:"library"` // карточка медиатеки; null — не в медиатеке
	File    string               `json:"file"`    // имя последнего файла без папок; "" — неизвестно
	Count   int                  `json:"count"`   // видеофайлов в раздаче; 0 — неизвестно
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	items, err := a.History.List(r.Context(), httpx.Device(r))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "история не читается: "+err.Error())
		return
	}
	hashes := make([]string, len(items))
	for i, it := range items {
		hashes[i] = it.Hash
	}
	refs, err := a.Catalog.ReleasesByHash(r.Context(), hashes)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "история не читается: "+err.Error())
		return
	}
	cards, hidden := map[string]library.CardSummary{}, map[string]bool{}
	if a.Library != nil {
		device := httpx.Device(r)
		if cards, err = a.Library.HistoryInfo(r.Context(), device, hashes); err == nil {
			hidden, err = a.Library.HiddenHashes(r.Context(), device, hashes)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "история не читается: "+err.Error())
			return
		}
	}
	out := make([]historyItem, 0, len(items))
	for _, it := range items {
		c, inLibrary := cards[it.Hash]
		// Скрытая категория на этом устройстве и файлы медиатеки, которых больше нет, — не показываются.
		if hidden[it.Hash] || (isLibraryHash(it.Hash) && !inLibrary) {
			continue
		}
		x := historyItem{Item: it}
		if inLibrary {
			x.Library = &c
		}
		if ref, ok := refs[it.Hash]; ok {
			x.Release = &ref
		}
		if isLibraryHash(it.Hash) {
			if names, n, err := a.Library.HistoryFiles(r.Context(), it.Hash); err == nil {
				x.File, x.Count = names[it.Last.Index], n
			}
			out = append(out, x)
			continue
		}
		out = append(out, x)
		i := len(out) - 1
		var ih metainfo.Hash
		if ih.FromHexString(it.Hash) != nil {
			continue
		}
		if fs, ok, err := a.Torrents.KnownFiles(r.Context(), ih); err == nil && ok {
			out[i].Count = len(fs)
			for _, f := range fs {
				if f.Index == it.Last.Index {
					out[i].File = f.Name[strings.LastIndexAny(f.Name, `/\`)+1:]
				}
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// isLibraryHash — «раздача» файлов из папок медиатеки: lib-<единица> (спека этапа 9, раздел 5.7).
func isLibraryHash(h string) bool { return reLibraryHash.MatchString(h) }

var reLibraryHash = regexp.MustCompile(`^lib-[1-9][0-9]*$`)

func historyHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	var ih metainfo.Hash
	h := strings.ToLower(r.PathValue("hash"))
	if isLibraryHash(h) {
		return h, true
	}
	if ih.FromHexString(h) != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный идентификатор раздачи")
		return "", false
	}
	return h, true
}

func (a *App) handleHistoryFiles(w http.ResponseWriter, r *http.Request) {
	h, ok := historyHash(w, r)
	if !ok {
		return
	}
	fs, err := a.History.Files(r.Context(), httpx.Device(r), h)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "история не читается: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"files": fs})
}

// handleHistoryPut — место от своего плеера ({positionSec, durationSec}) или отметка из пульта
// ({watched}).
func (a *App) handleHistoryPut(w http.ResponseWriter, r *http.Request) {
	h, ok := historyHash(w, r)
	if !ok {
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер файла")
		return
	}
	var req struct {
		PositionSec *float64 `json:"positionSec"`
		DurationSec *float64 `json:"durationSec"`
		Watched     *bool    `json:"watched"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	device := httpx.Device(r)
	switch {
	case req.PositionSec != nil && req.DurationSec != nil:
		err = a.History.SetPosition(r.Context(), device, h, index, *req.PositionSec, *req.DurationSec)
	case req.Watched != nil:
		err = a.History.SetWatched(r.Context(), device, h, index, *req.Watched)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "нужны positionSec и durationSec или watched")
		return
	}
	switch {
	case errors.Is(err, history.ErrBadPosition):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) handleHistoryDelete(w http.ResponseWriter, r *http.Request) {
	h, ok := historyHash(w, r)
	if !ok {
		return
	}
	if err := a.History.Remove(r.Context(), httpx.Device(r), h); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
