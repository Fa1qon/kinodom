package rutracker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

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
}

func (r *Rutracker) hasCredentials() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.login != "" && r.password != ""
}

// loggedIn — есть cookie сессии на текущем зеркале (у каждого зеркала своя сессия).
func (r *Rutracker) loggedIn() bool {
	u, err := url.Parse(r.forum.Mirror() + "/forum/")
	if err != nil {
		return false
	}
	for _, c := range r.jar.Cookies(u) {
		if c.Name == "bb_session" && c.Value != "" {
			return true
		}
	}
	return false
}

// Login входит на текущее зеркало. Попытка — одна: уже после первой неудачи Rutracker требует
// код с картинки, поэтому неверный пароль и капча блокируют вход до SetCredentials. Пароль не
// попадает ни в текст ошибки, ни в журнал: netx пишет только путь запроса.
func (r *Rutracker) Login(ctx context.Context) error {
	r.mu.Lock()
	login, password, block := r.login, r.password, r.loginBlock
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
	if err != nil {
		return err
	}
	err = parseLogin(p.Body)
	var ce *CaptchaError
	if errors.Is(err, ErrWrongPassword) || errors.As(err, &ce) {
		r.mu.Lock()
		r.loginBlock = err
		r.mu.Unlock()
	}
	return err
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
	if !r.loggedIn() {
		if err := r.Login(ctx); err != nil {
			return nil, err
		}
	}
	path := "/forum/tracker.php?nm=" + url.QueryEscape(cp1251(q)) + "&o=10&s=2"
	p, err := r.forumPage(ctx, path, "")
	if errors.Is(err, netx.ErrLoginRequired) {
		if err = r.Login(ctx); err == nil {
			p, err = r.forumPage(ctx, path, "")
		}
	}
	if err != nil {
		return nil, err
	}
	rs, err := parseSearch(p.Body)
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
// задан — сначала вход: вошедшему видны размер и раздающие; вход не удался — страница как для
// гостя (она открывается и так).
func (r *Rutracker) Details(ctx context.Context, topicID string) (source.Details, error) {
	if !isNumber(topicID) {
		return source.Details{}, fmt.Errorf("Rutracker: номер раздачи %q — не число", topicID)
	}
	if r.hasCredentials() && !r.loggedIn() {
		if err := r.Login(ctx); err != nil {
			r.log.Warn("Rutracker: вход не удался, раздача — как для гостя", "err", err)
		}
	}
	p, err := r.forumPage(ctx, "/forum/viewtopic.php?t="+topicID, "")
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
