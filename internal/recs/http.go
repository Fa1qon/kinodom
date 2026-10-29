package recs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"kinodom/internal/httpx"
)

// Router — маршруты API Kinodom (api.Server): всем, из домашней сети, только с этого ПК.
type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
	HandleLocal(pattern, module string, h http.Handler)
}

// Person — профиль семьи для «смотрим вместе».
type Person struct {
	Owner string `json:"owner"` // p:<номер>
	Name  string `json:"name"`
}

// Env — то, что рекомендациям даёт приложение.
type Env struct {
	Owner     func(r *http.Request) string // хозяин журнала запроса (профиль устройства)
	People    func(ctx context.Context) []Person
	Posters   func(ctx context.Context, src string) (string, error) // скачать постер в кэш → ключ /img/{key}
	KPKey     func() string
	KPBase    string // https://kinopoiskapiunofficial.tech (тесты подменяют)
	ModelPath string // куда ставится пакет модели
}

// Настройки рекомендаций — в общей таблице настроек (ключи свои, экран — на странице «Что посмотреть»).
const (
	keyLLMKey   = "recs.llm.key"
	keyLLMModel = "recs.llm.model"
	keyLLMBase  = "recs.llm.base"
)

func (s *Service) llm(ctx context.Context) *LLM {
	key, _, _ := s.DB.Setting(ctx, keyLLMKey)
	if key == "" {
		return nil
	}
	model, _, _ := s.DB.Setting(ctx, keyLLMModel)
	base, _, _ := s.DB.Setting(ctx, keyLLMBase)
	return &LLM{BaseURL: cmpOr(base, "https://polza.ai/api/v1"), Key: key, Model: cmpOr(model, "openai/gpt-6-luna")}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Register — маршруты рекомендаций.
func (s *Service) Register(rt Router, env Env) {
	h := &handlers{s: s, env: env}
	rt.Handle("GET /api/v1/recs", "", http.HandlerFunc(h.recs))
	rt.Handle("GET /api/v1/recs/status", "", http.HandlerFunc(h.status))
	rt.Handle("GET /api/v1/recs/taste", "", http.HandlerFunc(h.taste))
	rt.Handle("GET /api/v1/recs/quiz", "", http.HandlerFunc(h.quiz))
	rt.Handle("GET /api/v1/recs/films/{id}/poster", "", http.HandlerFunc(h.poster))
	rt.Handle("GET /api/v1/recs/films", "", http.HandlerFunc(h.cardsOf))
	rt.Handle("GET /api/v1/recs/wanted", "", http.HandlerFunc(h.wanted))
	rt.Handle("GET /api/v1/recs/genres", "", http.HandlerFunc(h.genreRails))
	rt.HandleHome("POST /api/v1/recs/rate", "", http.HandlerFunc(h.rate))
	rt.HandleHome("POST /api/v1/recs/mark", "", http.HandlerFunc(h.mark))
	rt.HandleHome("POST /api/v1/recs/import-kp", "", http.HandlerFunc(h.importKP))
	rt.HandleHome("PUT /api/v1/recs/config", "", http.HandlerFunc(h.config))
	rt.HandleLocal("POST /api/v1/recs/model", "", http.HandlerFunc(h.install))
}

type handlers struct {
	s   *Service
	env Env
}

// FilmOut — карточка для пульта.
type FilmOut struct {
	Film
	Type   string `json:"type"` // как у каталога «Кинопоиск»: FILM | TV_SERIES — для поиска раздач
	Poster string `json:"poster,omitempty"`
}

func filmOut(f Film) FilmOut {
	o := FilmOut{Film: f, Type: "FILM"}
	if f.Kind == "series" {
		o.Type = "TV_SERIES"
	}
	if f.Poster != "" {
		o.Poster = fmt.Sprintf("/api/v1/recs/films/%d/poster", f.KP)
	}
	return o
}

type recOut struct {
	Film    FilmOut            `json:"film"`
	Reason  string             `json:"reason"`
	Moods   []string           `json:"moods,omitempty"`
	Members map[string]float64 `json:"members,omitempty"`
}

func moodName(k string) string {
	for _, m := range Moods {
		if m.Key == k {
			return m.Name
		}
	}
	return k
}

func (h *handlers) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNoModel) {
		httpx.WriteError(w, http.StatusServiceUnavailable, "Рекомендации ещё не установлены: нужен пакет модели (recs-model.db)")
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, err.Error())
}

// recs — подборка: q — просьба словами, mood — оси, kind, with — профили для «вместе» (через запятую).
func (h *handlers) recs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := h.s.Model()
	if m == nil {
		h.fail(w, ErrNoModel)
		return
	}
	owner := h.env.Owner(r)
	qs := r.URL.Query()
	people := h.env.People(ctx)
	var names []string
	for _, p := range people {
		names = append(names, p.Name)
	}
	req := Request{}
	note := ""
	if text := strings.TrimSpace(qs.Get("q")); text != "" {
		var err error
		if req, err = h.s.llm(ctx).Parse(ctx, text, names, h.s.now().Year()); err != nil {
			note = "Модель не ответила — понял просьбу по словам"
		}
	}
	q := &req.Query
	q.Moods = append(q.Moods, qs["mood"]...)
	slices.Sort(q.Moods)
	q.Moods = slices.Compact(q.Moods)
	if k := qs.Get("kind"); k != "" {
		q.Kind = k
	}
	q.Genres = append(q.Genres, qs["genre"]...)
	q.Surprise = q.Surprise || qs.Get("surprise") == "1"
	q.Limit, _ = strconv.Atoi(qs.Get("limit"))
	q.Limit = min(max(q.Limit, 1), 90)
	if qs.Get("limit") == "" {
		q.Limit = 12
	}
	for _, t := range req.Like {
		if fs, _ := m.Search(ctx, t, 1); len(fs) > 0 {
			q.Like = append(q.Like, fs[0].KP)
		}
	}
	if like, err := strconv.Atoi(qs.Get("like")); err == nil && like > 0 {
		q.Like = append(q.Like, like)
	}
	group := []string{owner}
	add := func(o string) {
		if o != "" && !slices.Contains(group, o) {
			group = append(group, o)
		}
	}
	for _, o := range strings.Split(qs.Get("with"), ",") {
		if slices.ContainsFunc(people, func(p Person) bool { return p.Owner == o }) {
			add(o)
		}
	}
	for _, n := range req.With {
		for _, p := range people {
			if strings.EqualFold(p.Name, n) {
				add(p.Owner)
			}
		}
	}
	var list []Rec
	var err error
	if len(group) > 1 {
		list, err = h.s.Group(ctx, group, *q)
	} else {
		list, err = h.s.Recommend(ctx, owner, *q)
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	var because []int
	for _, x := range list {
		because = append(because, x.Because...)
	}
	titles, _ := h.s.films(ctx, m, because)
	out := []recOut{}
	var shown []int
	for _, x := range list {
		o := recOut{Film: filmOut(x.Film), Members: x.Members}
		for _, k := range x.Moods {
			o.Moods = append(o.Moods, moodName(k))
		}
		var bc []string
		for _, kp := range x.Because {
			if f, ok := titles[kp]; ok {
				bc = append(bc, "«"+f.Title+"»")
			}
		}
		switch {
		case len(group) > 1:
			o.Reason = "Подходит всем"
		case len(bc) > 0:
			o.Reason = "Похоже на " + strings.Join(bc, " и ")
		case len(o.Moods) > 0:
			o.Reason = strings.Join(o.Moods, ", ")
		case x.Fill:
			o.Reason = "Из лучшего в жанре"
		default:
			o.Reason = "Любят зрители с похожим вкусом"
		}
		out = append(out, o)
		shown = append(shown, x.Film.KP)
	}
	var show []Film
	for _, x := range list {
		show = append(show, x.Film)
	}
	h.s.needCards(show) // фильмы без карточки — дозапросить: постер появится при следующем обновлении
	if qs.Get("shown") != "0" {
		h.s.MarkShown(ctx, owner, shown)
	}
	httpx.WriteJSON(w, 200, map[string]any{"owner": owner, "group": group, "query": q, "note": note, "items": out})
}

func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	m := h.s.Model()
	out := map[string]any{"installed": m != nil, "moods": Moods, "people": h.env.People(r.Context()), "owner": h.env.Owner(r)}
	if m != nil {
		out["model"] = m.Info
	}
	key, _, _ := h.s.DB.Setting(r.Context(), keyLLMKey)
	model, _, _ := h.s.DB.Setting(r.Context(), keyLLMModel)
	out["llm"] = map[string]any{"configured": key != "", "model": cmpOr(model, "openai/gpt-6-luna")}
	out["kpKey"] = h.env.KPKey != nil && h.env.KPKey() != ""
	httpx.WriteJSON(w, 200, out)
}

// taste — что Kinodom знает о вкусе человека: прозрачность («почему мне это советуют»).
func (h *handlers) taste(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := h.env.Owner(r)
	sg, err := h.s.signals(ctx, owner)
	if err != nil {
		h.fail(w, err)
		return
	}
	prof := sg.Build(h.s.now())
	type item struct {
		Film   FilmOut `json:"film"`
		Weight float64 `json:"weight"`
		Why    string  `json:"why"`
	}
	var ids []int
	for kp := range prof {
		ids = append(ids, kp)
	}
	var fs map[int]Film
	if m := h.s.Model(); m != nil {
		fs, _ = h.s.films(ctx, m, ids)
	}
	var liked, disliked []item
	var unknown []Film // фильмов нет ни в пакете, ни в карточках — дозапросить (оценки импорта до 2026-10-29)
	defer func() { h.s.needCards(unknown) }()
	for kp, x := range prof {
		f, ok := fs[kp]
		if !ok {
			f = Film{KP: kp, Title: "Загружается…"}
			unknown = append(unknown, f)
		}
		it := item{Film: filmOut(f), Weight: x, Why: why(sg, kp)}
		if x > 0 {
			liked = append(liked, it)
		} else if x < 0 {
			disliked = append(disliked, it)
		}
	}
	slices.SortFunc(liked, func(a, b item) int { return cmpDesc(a.Weight, b.Weight) })
	slices.SortFunc(disliked, func(a, b item) int { return cmpDesc(b.Weight, a.Weight) })
	fav := []GenreScore{}
	if gs, err := h.s.GenreTaste(ctx, owner); err == nil {
		for _, g := range gs {
			if g.Lift >= 1 && len(fav) < 8 {
				fav = append(fav, g)
			}
		}
	}
	httpx.WriteJSON(w, 200, map[string]any{"owner": owner, "ratings": len(sg.Ratings), "watched": len(sg.Watches), "genres": fav,
		"liked": liked[:min(30, len(liked))], "disliked": disliked[:min(30, len(disliked))]})
}

// why — откуда вклад: оценка, досмотрел, бросил, удалил.
func why(sg Signals, kp int) string {
	if v, ok := sg.Ratings[kp]; ok {
		return fmt.Sprintf("оценка %d", v)
	}
	if w := sg.Watches[kp]; w != nil {
		switch {
		case w.files >= 2:
			return fmt.Sprintf("досмотрено серий: %d", w.finished)
		case w.times >= 2:
			return fmt.Sprintf("пересмотрен %d раза", w.times)
		case w.finished > 0 && w.manual:
			return "отмечен просмотренным"
		case w.finished > 0:
			return "досмотрен"
		case w.deleted:
			return fmt.Sprintf("удалён на %d%%", int(w.seen*100))
		default:
			return fmt.Sprintf("брошен на %d%%", int(w.seen*100))
		}
	}
	switch sg.Marks[kp] {
	case "notmine":
		return "«не моё»"
	case "want":
		return "хочу посмотреть"
	}
	return ""
}

// quiz — фильм для быстрой оценки: популярный, ещё не известный человеку.
func (h *handlers) quiz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := h.s.Model()
	if m == nil {
		h.fail(w, ErrNoModel)
		return
	}
	owner := h.env.Owner(r)
	sg, err := h.s.signals(ctx, owner)
	if err != nil {
		h.fail(w, err)
		return
	}
	pop, err := m.Popular(ctx, 400)
	if err != nil {
		h.fail(w, err)
		return
	}
	skip := map[string]bool{}
	for _, s := range strings.Split(r.URL.Query().Get("skip"), ",") {
		skip[s] = true
	}
	var pick []int
	for _, kp := range pop {
		if !sg.Seen(kp) && sg.Marks[kp] == "" && !skip[strconv.Itoa(kp)] {
			pick = append(pick, kp)
			if len(pick) == 8 {
				break
			}
		}
	}
	fs, _ := h.s.films(ctx, m, pick)
	out := []FilmOut{}
	var show []Film
	for _, kp := range pick {
		out = append(out, filmOut(fs[kp]))
		show = append(show, fs[kp])
	}
	h.s.needCards(show)
	httpx.WriteJSON(w, 200, map[string]any{"rated": len(sg.Ratings), "items": out})
}

// genreRails — подборка по жанрам: строки любимых жанров первыми; kind — только фильмы или сериалы.
func (h *handlers) genreRails(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := h.s.Model()
	if m == nil {
		h.fail(w, ErrNoModel)
		return
	}
	owner := h.env.Owner(r)
	rails, taste, err := h.s.GenreRails(ctx, owner, Query{Kind: r.URL.Query().Get("kind")})
	if err != nil {
		h.fail(w, err)
		return
	}
	type railOut struct {
		Genre string   `json:"genre"`
		Title string   `json:"title"`
		Lift  float64  `json:"lift"`
		Items []recOut `json:"items"`
	}
	out := []railOut{}
	var show []Film
	for _, rl := range rails {
		ro := railOut{Genre: rl.Genre, Title: rl.Title, Lift: rl.Lift}
		var because []int
		for _, x := range rl.Recs {
			because = append(because, x.Because...)
		}
		titles, _ := h.s.films(ctx, m, because)
		for _, x := range rl.Recs {
			o := recOut{Film: filmOut(x.Film), Reason: "Любят зрители с похожим вкусом"}
			var bc []string
			for _, kp := range x.Because {
				if f, ok := titles[kp]; ok {
					bc = append(bc, "«"+f.Title+"»")
				}
			}
			if len(bc) > 0 {
				o.Reason = "Похоже на " + strings.Join(bc, " и ")
			}
			ro.Items = append(ro.Items, o)
			show = append(show, x.Film)
		}
		out = append(out, ro)
	}
	h.s.needCards(show)
	if taste == nil {
		taste = []GenreScore{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"rails": out, "taste": taste})
}

// wanted — «Хочу посмотреть»: отметки «+» в подборке (новые первыми) и «Смотреть позже» Kinodom; оценённое
// и отмеченное «видел» сюда не попадает. Фильм без карточки — заготовкой, карточку дозапросит очередь.
func (h *handlers) wanted(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := h.s.Model()
	if m == nil {
		h.fail(w, ErrNoModel)
		return
	}
	owner := h.env.Owner(r)
	var ids []int
	rows, err := h.s.DB.R.QueryContext(ctx, `SELECT m.kp_id FROM rec_marks m
		WHERE m.owner = ? AND m.mark = 'want' AND NOT EXISTS (SELECT 1 FROM rec_ratings r WHERE r.owner = m.owner AND r.kp_id = m.kp_id)
		ORDER BY m.at DESC`, owner)
	if err != nil {
		h.fail(w, err)
		return
	}
	for rows.Next() {
		var kp int
		rows.Scan(&kp)
		ids = append(ids, kp)
	}
	rows.Close()
	sg, err := h.s.signals(ctx, owner)
	if err != nil {
		h.fail(w, err)
		return
	}
	if h.s.Wanted != nil {
		for _, kp := range h.s.Wanted(ctx, owner) {
			if !slices.Contains(ids, kp) && !sg.Seen(kp) {
				ids = append(ids, kp)
			}
		}
	}
	fs, err := h.s.films(ctx, m, ids)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []FilmOut{}
	var need []Film
	for _, kp := range ids {
		f, ok := fs[kp]
		if !ok {
			f = Film{KP: kp, Kind: "film", Title: "Загружается…"}
		}
		need = append(need, f)
		out = append(out, filmOut(f))
	}
	h.s.needCards(need)
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}

// cardsOf — карточки фильмов ?ids=1,2,3: экран подставляет постеры, дозапрошенные после показа подборки
// (повторный запрос подборки переставил бы её: показанное уходит ниже).
func (h *handlers) cardsOf(w http.ResponseWriter, r *http.Request) {
	m := h.s.Model()
	if m == nil {
		h.fail(w, ErrNoModel)
		return
	}
	var ids []int
	for _, x := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id, err := strconv.Atoi(x); err == nil && id > 0 && len(ids) < 100 {
			ids = append(ids, id)
		}
	}
	fs, err := h.s.films(r.Context(), m, ids)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []FilmOut{}
	for _, id := range ids {
		if f, ok := fs[id]; ok {
			out = append(out, filmOut(f))
		}
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}

func (h *handlers) poster(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	m := h.s.Model()
	if m == nil || h.env.Posters == nil {
		httpx.WriteError(w, http.StatusNotFound, "постера нет")
		return
	}
	f, ok := h.s.film(r.Context(), m, id)
	if !ok || f.Poster == "" {
		httpx.WriteError(w, http.StatusNotFound, "постера нет")
		return
	}
	key, err := h.env.Posters(r.Context(), f.Poster)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "постер не скачался: "+err.Error())
		return
	}
	to := "/img/" + key
	if width := r.URL.Query().Get("w"); width != "" && strings.Trim(width, "0123456789") == "" {
		to += "?w=" + width
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	http.Redirect(w, r, to, http.StatusFound)
}

func (h *handlers) rate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		KP    int `json:"kpId"`
		Score int `json:"score"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	if in.KP <= 0 || in.Score < 1 || in.Score > 10 {
		httpx.WriteError(w, 400, "нужны kpId и оценка от 1 до 10")
		return
	}
	if err := h.s.Rate(r.Context(), h.env.Owner(r), in.KP, in.Score, "kinodom"); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) mark(w http.ResponseWriter, r *http.Request) {
	var in struct {
		KP   int    `json:"kpId"`
		Mark string `json:"mark"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	if in.KP <= 0 || !slices.Contains([]string{"", "notmine", "seen", "want"}, in.Mark) {
		httpx.WriteError(w, 400, "отметка: notmine, seen, want или пусто")
		return
	}
	if err := h.s.Mark(r.Context(), h.env.Owner(r), in.KP, in.Mark); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var reKPUser = regexp.MustCompile(`(?:kinopoisk\.ru/user/)?(\d{3,})`)

func (h *handlers) importKP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Profile string `json:"profile"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	m := reKPUser.FindStringSubmatch(in.Profile)
	if m == nil {
		httpx.WriteError(w, 400, "нужна ссылка на профиль: kinopoisk.ru/user/123456/")
		return
	}
	key := ""
	if h.env.KPKey != nil {
		key = h.env.KPKey()
	}
	votes, err := KPVotes(r.Context(), cmpOr(h.env.KPBase, "https://kinopoiskapiunofficial.tech"), key, m[1])
	if err != nil && len(votes) == 0 {
		httpx.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	owner := h.env.Owner(r)
	for _, v := range votes {
		if err := h.s.Rate(r.Context(), owner, v.KP, v.Score, "kp"); err != nil {
			h.fail(w, err)
			return
		}
		h.s.saveCard(r.Context(), v.KP, v.Card) // название и постер — «Мой вкус» и «Похожие» показывают фильм, а не номер
	}
	httpx.WriteJSON(w, 200, map[string]any{"imported": len(votes)})
}

func (h *handlers) config(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LLMKey   *string `json:"llmKey"`
		LLMModel *string `json:"llmModel"`
		LLMBase  *string `json:"llmBase"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	for k, v := range map[string]*string{keyLLMKey: in.LLMKey, keyLLMModel: in.LLMModel, keyLLMBase: in.LLMBase} {
		if v != nil {
			if err := h.s.DB.SetSetting(r.Context(), k, strings.TrimSpace(*v)); err != nil {
				h.fail(w, err)
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// install — поставить пакет модели из файла на этом ПК (только с самого ПК: путь — его диск).
func (h *handlers) install(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	if err := InstallModel(in.Path, h.env.ModelPath); err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	m, err := OpenModel(h.env.ModelPath)
	if err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	h.s.SetModel(m)
	httpx.WriteJSON(w, 200, map[string]any{"model": m.Info})
}

// InstallModel копирует пакет в папку данных: сначала во временный файл, проверка, потом замена.
func InstallModel(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("файл пакета не открылся: %w", err)
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	m, err := OpenModel(tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	ok := m.Info["version"] != ""
	m.Close()
	if !ok {
		os.Remove(tmp)
		return errors.New("это не пакет рекомендаций")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	os.Remove(dst + ".old")
	os.Rename(dst, dst+".old") // открытый пакет на Windows не перезаписать — старый уходит в сторону
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	os.Remove(dst + ".old")
	return nil
}
