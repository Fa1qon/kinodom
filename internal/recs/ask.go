package recs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Moods — оси настроения (одни для пакета модели, разбора просьбы и пульта).
var Moods = []struct{ Key, Name, Hint string }{
	{"cozy", "уютное", "тёплое, спокойное, приятно провести вечер"},
	{"light", "лёгкое", "не грузит, можно смотреть вполглаза"},
	{"funny", "смешное", "комедия, много юмора"},
	{"tense", "напряжённое", "держит в напряжении, саспенс, триллер"},
	{"scary", "страшное", "пугает, хоррор"},
	{"dark", "мрачное", "тяжёлое, жестокое, безысходное"},
	{"touching", "трогательное", "до слёз, грустное, душевное"},
	{"uplifting", "вдохновляющее", "заряжает, после него хочется жить"},
	{"romantic", "романтичное", "про любовь"},
	{"atmospheric", "атмосферное", "красивое, погружает в мир"},
	{"thoughtful", "умное", "заставляет думать, философское"},
	{"twist", "с неожиданным финалом", "поворот, загадка"},
	{"action", "динамичное", "экшн, погони, всё быстро"},
	{"slow", "неспешное", "созерцательное, медленное"},
	{"family", "для всей семьи", "можно с детьми"},
	{"weird", "необычное", "странное, сюрреалистичное, артхаус"},
	{"epic", "масштабное", "эпик, большая история"},
}

// Genres — жанры как на Кинопоиске: модель выбирает только из них.
var Genres = []string{"аниме", "биография", "боевик", "вестерн", "военный", "детектив", "детский", "документальный",
	"драма", "история", "комедия", "криминал", "мелодрама", "музыка", "мультфильм", "мюзикл", "приключения",
	"семейный", "спорт", "триллер", "ужасы", "фантастика", "фэнтези"}

// Request — разобранная просьба.
type Request struct {
	Query Query    `json:"query"`
	With  []string `json:"with,omitempty"` // имена профилей семьи
	Like  []string `json:"like,omitempty"` // «как …» — названия
}

var moodWords = map[string][]string{
	"cozy": {"уютн", "тепл", "душевн", "спокойн"}, "light": {"лёгк", "легк", "ненапряж", "вполглаза", "расслаб"},
	"funny": {"смешн", "комеди", "поржать", "посмеяться", "юмор"}, "tense": {"напряж", "триллер", "саспенс"},
	"scary": {"страшн", "ужас", "хоррор", "жутк"}, "dark": {"мрачн", "тяжел", "тяжёл", "жесток"},
	"touching": {"трогат", "до слез", "до слёз", "поплакать", "грустн"}, "uplifting": {"вдохнов", "мотивир", "заряж"},
	"romantic": {"романт", "про любовь", "мелодрам"}, "atmospheric": {"атмосфер", "красив"},
	"thoughtful": {"умн", "подумать", "философ", "заставля"}, "twist": {"неожидан", "твист", "поворот", "загадк", "мозголом"},
	"action": {"динами", "экшн", "экшен", "боевик", "погон"}, "slow": {"неспеш", "медлен", "созерцат"},
	"family": {"семь", "с детьми", "ребен", "ребён", "детск"}, "weird": {"необычн", "странн", "артхаус", "сюр"},
	"epic": {"эпич", "масштаб", "эпик"},
}

func negated(before string) bool {
	before = strings.TrimRight(before, " ")
	return strings.HasSuffix(before, " без") || strings.HasSuffix(before, " не") || before == "без" || before == "не"
}

// ParseRules — разбор без модели: слова → оси, «без …» — запрет, имена профилей, «как „…“».
func ParseRules(text string, people []string) Request {
	t := strings.ToLower(text)
	var r Request
	for _, m := range Moods {
		for _, w := range moodWords[m.Key] {
			if i := strings.Index(t, w); i >= 0 && !negated(t[:i]) {
				r.Query.Moods = append(r.Query.Moods, m.Key)
				break
			}
		}
	}
	if len(r.Query.Moods) > 3 {
		r.Query.Moods = r.Query.Moods[:3]
	}
	switch {
	case strings.Contains(t, "сериал"):
		r.Query.Kind = "series"
	case strings.Contains(t, "фильм"):
		r.Query.Kind = "film"
	}
	if strings.Contains(t, "коротк") || strings.Contains(t, "недолг") || strings.Contains(t, "после работы") {
		r.Query.MaxDuration = 110
	}
	if strings.Contains(t, "удиви") || strings.Contains(t, "малоизвест") {
		r.Query.Surprise = true
	}
	if slices.Contains(r.Query.Moods, "family") {
		r.Query.NoGenres = append(r.Query.NoGenres, "ужасы")
	}
	for _, g := range Genres {
		stem := string([]rune(g)[:min(len([]rune(g)), 4)])
		if strings.Contains(t, "без "+stem) || strings.Contains(t, "не "+stem) {
			r.Query.NoGenres = append(r.Query.NoGenres, g)
		}
	}
	for _, name := range people {
		n := []rune(strings.ToLower(name))
		if len(n) >= 3 && strings.Contains(t, string(n[:len(n)-1])) {
			r.With = append(r.With, name)
		}
	}
	if s, e := strings.IndexAny(text, "«\""), strings.LastIndexAny(text, "»\""); s >= 0 && e > s {
		r.Like = append(r.Like, strings.Trim(text[s:e], "«»\" "))
	}
	return r
}

// LLM — OpenAI-совместимый чат (polza.ai и подобные): только разбор просьбы; фильмы выбирает модель рекомендаций.
type LLM struct {
	BaseURL, Key, Model string
	HTTP                *http.Client
}

const parseSystem = `Ты разбираешь просьбу человека, который выбирает фильм или сериал. Верни только JSON:
{"moods": [ключи настроения, не больше трёх], "genres": [жанры], "noGenres": [жанры, которых избегать], "kind": "film"|"series"|"",
 "maxDuration": минуты или 0, "yearFrom": год или 0, "yearTo": год или 0, "surprise": true, если просят необычное,
 "with": [имена из семьи, с кем смотрят], "like": [названия из «как …», «типа …»]}
Ключи настроения: %s. Жанры: %s. Семья: %s. Сейчас %d год.
«Короткое», «после работы» — maxDuration 110. С детьми — moods family, noGenres ужасы. Чего нет в просьбе — пусто.`

// Parse — разбор моделью; ошибка — разбор правилами (и ошибка для журнала).
func (l *LLM) Parse(ctx context.Context, text string, people []string, year int) (Request, error) {
	rules := ParseRules(text, people)
	if l == nil || l.Key == "" {
		return rules, nil
	}
	var keys []string
	for _, m := range Moods {
		keys = append(keys, m.Key+" ("+m.Name+")")
	}
	body, _ := json.Marshal(map[string]any{
		"model": l.Model, "temperature": 0.2, "response_format": map[string]string{"type": "json_object"},
		"reasoning": map[string]string{"effort": "none"}, // рассуждения оплачиваются как ответ, разбору они не нужны
		"messages": []map[string]string{
			{"role": "system", "content": fmt.Sprintf(parseSystem, strings.Join(keys, ", "), strings.Join(Genres, ", "), strings.Join(people, ", "), year)},
			{"role": "user", "content": text}},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.Key)
	client := l.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return rules, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(raw, &r) != nil || len(r.Choices) == 0 {
		return rules, fmt.Errorf("LLM %d: %.200s", resp.StatusCode, raw)
	}
	text = r.Choices[0].Message.Content
	if a, b := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}'); a >= 0 && b > a {
		text = text[a : b+1]
	}
	var out struct {
		Query
		With []string `json:"with"`
		Like []string `json:"like"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return rules, errors.New("LLM ответила не JSON")
	}
	got := Request{Query: out.Query, With: out.With, Like: out.Like}
	sanitize(&got, people)
	return got, nil
}

// sanitize выбрасывает то, чего нет в словарях: модель не протащит выдуманный ключ.
func sanitize(r *Request, people []string) {
	r.Query.Moods = slices.DeleteFunc(r.Query.Moods, func(m string) bool {
		return !slices.ContainsFunc(Moods, func(x struct{ Key, Name, Hint string }) bool { return x.Key == m })
	})
	if len(r.Query.Moods) > 3 {
		r.Query.Moods = r.Query.Moods[:3]
	}
	known := func(g string) bool { return slices.Contains(Genres, strings.ToLower(g)) }
	r.Query.Genres = slices.DeleteFunc(r.Query.Genres, func(g string) bool { return !known(g) })
	r.Query.NoGenres = slices.DeleteFunc(r.Query.NoGenres, func(g string) bool { return !known(g) })
	if r.Query.Kind != "film" && r.Query.Kind != "series" {
		r.Query.Kind = ""
	}
	r.Query.Like, r.Query.Limit = nil, 0
	r.With = slices.DeleteFunc(r.With, func(n string) bool {
		return !slices.ContainsFunc(people, func(c string) bool { return strings.EqualFold(c, n) })
	})
}

// ---- оценки с Кинопоиска (kinopoiskapiunofficial.tech, тот же ключ, что в настройках Kinodom) ----

// KPVote — оценка из профиля.
type KPVote struct{ KP, Score int }

// KPVotes — оценки профиля (сервис отдаёт около 1500 последних, по 20 на страницу).
func KPVotes(ctx context.Context, base, key, user string) ([]KPVote, error) {
	if key == "" {
		return nil, errors.New("нужен ключ Кинопоиска в настройках (Параметры → Кинопоиск)")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var out []KPVote
	for page, pages := 1, 1; page <= pages && page <= 100; page++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/kp_users/%s/votes?page=%d", base, user, page), nil)
		req.Header.Set("X-API-KEY", key)
		resp, err := client.Do(req)
		if err != nil {
			return out, err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		switch resp.StatusCode {
		case 200:
		case 404:
			return out, errors.New("профиль не найден или оценки скрыты в настройках приватности Кинопоиска")
		case 402, 429:
			return out, errors.New("суточный лимит ключа Кинопоиска исчерпан — попробуйте завтра")
		default:
			return out, fmt.Errorf("Кинопоиск: ответ %d", resp.StatusCode)
		}
		var v struct {
			TotalPages int `json:"totalPages"`
			Items      []struct {
				KinopoiskID int `json:"kinopoiskId"`
				UserRating  int `json:"userRating"`
			} `json:"items"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return out, err
		}
		pages = v.TotalPages
		for _, it := range v.Items {
			if it.UserRating >= 1 && it.UserRating <= 10 {
				out = append(out, KPVote{it.KinopoiskID, it.UserRating})
			}
		}
	}
	return out, nil
}
