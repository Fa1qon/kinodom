package torrents

import (
	"net/http"
	"strconv"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/httpx"
)

// StreamHandler отдаёт файл раздачи с поддержкой Range: GET /stream/{hash}/{index}/{name}.
// Перемотка — это новый Range-запрос; движок сам качает нужное место первым.
func (s *Service) StreamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih, ok := parseHash(w, r)
		if !ok {
			return
		}
		t, found := s.eng.cl.Torrent(ih)
		if !found {
			httpx.WriteError(w, http.StatusNotFound, ErrNotOpen.Error())
			return
		}
		select {
		case <-t.GotInfo():
		case <-r.Context().Done():
			return
		}
		index, err := strconv.Atoi(r.PathValue("index"))
		files := t.Files()
		if err != nil || index < 0 || index >= len(files) {
			httpx.WriteError(w, http.StatusNotFound, ErrNoSuchFile.Error())
			return
		}
		f := files[index]
		s.activeStreams.Add(1)
		defer s.activeStreams.Add(-1)
		defer s.openReader(ih, index)()
		defer s.keeper.Acquire()() // ПК не засыпает, пока смотрят (спека, раздел 9)
		if err := s.reg.TouchStream(r.Context(), ih, index, s.now()); err != nil {
			s.log.Warn("не удалось отметить просмотр", "err", err)
		}

		// Свой читатель на каждый запрос: у каждого телевизора своя позиция.
		rd := f.NewReader()
		defer rd.Close()
		rd.SetContext(r.Context()) // зритель ушёл — чтение прерывается и не держит приоритеты кусков
		rd.SetResponsive()         // отдавать данные, не дожидаясь проверки целого куска — быстрая перемотка
		rd.SetReadahead(int64(headSeconds * s.bitrateOf(ih, index, f.Length())))

		ct := videoTypes[extOf(f.DisplayPath())]
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct) // иначе ServeContent стал бы угадывать тип по байтам
		http.ServeContent(w, r, "", time.Time{}, rd)
	})
}

// ActiveStreams — сколько потоков идёт прямо сейчас (запрет сна и урезание отдачи — этап 6).
func (s *Service) ActiveStreams() int { return int(s.activeStreams.Load()) }

func (s *Service) bitrateOf(ih metainfo.Hash, index int, size int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.sessions[ih]; ok {
		if p, ok := ss.prepared[index]; ok {
			return p.bitrate
		}
	}
	return estimateBitrate(size, false)
}

// parseHash читает {hash} из пути; при ошибке сам отвечает 400.
func parseHash(w http.ResponseWriter, r *http.Request) (metainfo.Hash, bool) {
	var ih metainfo.Hash
	if err := ih.FromHexString(r.PathValue("hash")); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный идентификатор раздачи")
		return ih, false
	}
	return ih, true
}

// openReader отмечает открытый поток к файлу (такой файл «сейчас смотрят» и не удаляется);
// возвращает отметку закрытия.
func (s *Service) openReader(ih metainfo.Hash, index int) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[ih]
	if ss == nil {
		return func() {}
	}
	ss.readers[index]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		ss.readers[index]--
	}
}

// streaming — к какому-то файлу раздачи открыт поток. Вызывать под s.mu.
func (ss *session) streaming() bool {
	for _, n := range ss.readers {
		if n > 0 {
			return true
		}
	}
	return false
}
