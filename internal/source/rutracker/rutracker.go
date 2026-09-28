package rutracker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/htmltext"
)

var _ source.Source = (*Rutracker)(nil)

// ErrNoCredentials — логин и пароль не заданы: поиск недоступен (он только для вошедших).
var ErrNoCredentials = errors.New("Rutracker: не заданы логин и пароль — поиск недоступен")

// searchCats — категории, в которых ищет поиск (спека, раздел 6): «Кино, Видео и ТВ»,
// «Сериалы», «Документалистика и юмор», «Обучающие видео».
var searchCats = []string{"2", "18", "20", "10"}

// SetCredentials — логин и пароль из настроек. Снимает запрет на вход после неудачи.
func (r *Rutracker) SetCredentials(login, password string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.login, r.password, r.loginBlock = login, password, nil
	r.loginRetryAt, r.loginWarned = time.Time{}, ""
	r.credGen++
}

func (r *Rutracker) hasCredentials() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.login != "" && r.password != ""
}

// session — значение cookie сессии на текущем зеркале (у каждого зеркала своя); "" — не вошли.
func (r *Rutracker) session() string {
	u, err := url.Parse(r.forum.Mirror() + "/forum/")
	if err != nil {
		return ""
	}
	for _, c := range r.jar.Cookies(u) {
		if c.Name == "bb_session" {
			return c.Value
		}
	}
	return ""
}

func (r *Rutracker) loggedIn() bool { return r.session() != "" }

// Login входит на текущее зеркало. Попытка — одна: уже после первой неудачи Rutracker требует
// код с картинки, поэтому неверный пароль и капча блокируют вход до SetCredentials. Пароль не
// попадает ни в текст ошибки, ни в журнал: netx пишет только путь запроса.
func (r *Rutracker) Login(ctx context.Context) error {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	return r.doLogin(ctx)
}

// ensureLogin входит, если сессии нет (stale == "") или форум отверг именно её (stale — её
// значение). Одновременные вызовы ждут одного входа: следующий увидит новую сессию и входить
// не станет, а после неудачи получит запрет без новой попытки.
func (r *Rutracker) ensureLogin(ctx context.Context, stale string) error {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	if s := r.session(); s != "" && s != stale {
		return nil
	}
	return r.doLogin(ctx)
}

// doLogin — сам вход; вызывается под loginMu.
func (r *Rutracker) doLogin(ctx context.Context) error {
	r.mu.Lock()
	login, password, block, gen := r.login, r.password, r.loginBlock, r.credGen
	r.mu.Unlock()
	if login == "" || password == "" {
		return ErrNoCredentials
	}
	if block != nil {
		return block
	}
	form := "redirect=index.php" +
		"&login_username=" + url.QueryEscape(cp1251(login)) +
		"&login_password=" + url.QueryEscape(cp1251(password)) +
		"&login=" + url.QueryEscape(cp1251("Вход"))
	p, err := r.forumPage(ctx, "/forum/login.php", form, netx.WithoutClassify())
	if err == nil {
		err = parseLogin(p.Body)
	}
	var ce *CaptchaError
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.loginRetryAt = time.Time{}
	case errors.Is(err, ErrWrongPassword) || errors.As(err, &ce):
		if r.credGen == gen { // пароль не меняли, пока шёл вход
			r.loginBlock = err
		}
	case ctx.Err() == nil:
		// Сеть, форум, непонятный ответ — временно: страница раздачи минуту не входит сама.
		r.loginRetryAt = r.now().Add(loginRetryAfter)
	}
	return err
}

// loginRetryAfter — пауза автоматического входа страницы раздачи после временной неудачи.
const loginRetryAfter = time.Minute

// mayAutoLogin — странице раздачи можно войти самой: логин задан, и после временной неудачи
// входа прошла минута. Поиск входит всегда: там ответа ждёт человек.
func (r *Rutracker) mayAutoLogin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.login != "" && r.password != "" && !r.now().Before(r.loginRetryAt)
}

// noteLogin пишет в журнал неудачу входа страницы раздачи — один раз на каждую новую причину,
// а не на каждую из сотен раздач каталога. nil — вход удался, о причине можно забыть.
func (r *Rutracker) noteLogin(err error) {
	text := ""
	if err != nil {
		text = err.Error()
	}
	r.mu.Lock()
	same := text == r.loginWarned
	r.loginWarned = text
	r.mu.Unlock()
	if err != nil && !same {
		r.log.Warn("Rutracker: вход не удался, раздача — как для гостя", "err", err)
	}
}

// Search ищет по видеокатегориям одним запросом, по раздающим (спека, разделы 6 и 7). Нужен вход:
// гостя форум отправляет на страницу входа; истёкшая сессия — тихий повторный вход.
func (r *Rutracker) Search(ctx context.Context, query string) ([]source.Release, error) {
	q := htmltext.Clean(query)
	if rs := []rune(q); len(rs) > 100 {
		q = strings.TrimSpace(string(rs[:100]))
	}
	if q == "" {
		return nil, errors.New("Rutracker: пустой поисковый запрос")
	}
	if !r.hasCredentials() {
		return nil, ErrNoCredentials
	}
	tree, err := r.forumTree(ctx)
	if err != nil {
		return nil, err
	}
	rs, err := r.trackerPage(ctx, url.Values{"nm": {q}, "o": {"10"}, "s": {"2"}})
	if err != nil {
		return nil, err
	}
	allowed := tree.forumsUnder(searchCats...)
	out := rs[:0]
	for _, x := range rs {
		if allowed[x.CategoryID] {
			out = append(out, x)
		}
	}
	sortBySeeders(out)
	return out, nil
}

// Details — страница раздачи: название, описание, постер, id Кинопоиска, magnet. Если логин
// задан — сначала вход: вошедшему видны размер и раздающие. Вход не удался — страница как для
// гостя (она открывается и так); после временной неудачи вход минуту не повторяется. Истёкшая
// сессия — тихий повторный вход (спека, раздел 16).
func (r *Rutracker) Details(ctx context.Context, topicID string) (source.Details, error) {
	if !isNumber(topicID) {
		return source.Details{}, fmt.Errorf("Rutracker: номер раздачи %q — не число", topicID)
	}
	if r.mayAutoLogin() && !r.loggedIn() {
		r.noteLogin(r.ensureLogin(ctx, ""))
	}
	path := "/forum/viewtopic.php?t=" + topicID
	stale := r.session()
	p, err := r.forumPage(ctx, path, "")
	if errors.Is(err, netx.ErrLoginRequired) && r.mayAutoLogin() {
		if err = r.ensureLogin(ctx, stale); err == nil {
			p, err = r.forumPage(ctx, path, "")
		}
	}
	if err != nil {
		return source.Details{}, err
	}
	d, err := parseTopic(p.Body)
	if err != nil {
		return source.Details{}, err
	}
	d.TopicID = topicID
	return d, nil
}

// SearchRaw — страница поиска tracker.php с параметрами как есть, без фильтра по категориям:
// для проверки параметров поиска вживую (kinodom source rutracker search-raw).
func (r *Rutracker) SearchRaw(ctx context.Context, params url.Values) ([]source.Release, error) {
	if !r.hasCredentials() {
		return nil, ErrNoCredentials
	}
	return r.trackerPage(ctx, params)
}

// trackerPage — результаты tracker.php со входом: гостя форум отправляет на страницу входа,
// истёкшая сессия — тихий повторный вход.
func (r *Rutracker) trackerPage(ctx context.Context, params url.Values) ([]source.Release, error) {
	if err := r.ensureLogin(ctx, ""); err != nil {
		return nil, err
	}
	path := "/forum/tracker.php?" + queryCP1251(params)
	stale := r.session()
	p, err := r.forumPage(ctx, path, "")
	if errors.Is(err, netx.ErrLoginRequired) {
		if err = r.ensureLogin(ctx, stale); err == nil {
			p, err = r.forumPage(ctx, path, "")
		}
	}
	if err != nil {
		return nil, err
	}
	return parseSearch(p.Body)
}

// queryCP1251 — строка запроса со значениями в windows-1251, как их шлёт форма форума; ключи —
// по алфавиту.
func queryCP1251(v url.Values) string {
	parts := make([]string, 0, len(v))
	for _, k := range slices.Sorted(maps.Keys(v)) {
		for _, x := range v[k] {
			parts = append(parts, url.QueryEscape(cp1251(k))+"="+url.QueryEscape(cp1251(x)))
		}
	}
	return strings.Join(parts, "&")
}
