package rutracker

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/rutracker/rutrackertest"
)

func withCreds(login, password string) func(*Options) {
	return func(o *Options) { o.Login, o.Password = login, password }
}

func TestSearchLogsInOnceAndFilters(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	r := newRutracker(t, s, withCreds("user", "pass"))
	rs, err := r.Search(ctx, "космос")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 || s.Logins() != 1 {
		t.Fatalf("найдено %d, входов %d", len(rs), s.Logins())
	}
	tree, _ := r.forumTree(ctx)
	allowed := tree.forumsUnder("2", "18", "20", "10")
	for _, x := range rs {
		if !allowed[x.CategoryID] {
			t.Fatalf("раздача из раздела вне поиска: %+v", x)
		}
	}
	if !slices.IsSortedFunc(rs, func(a, b source.Release) int { return b.Seeders - a.Seeders }) {
		t.Fatal("не по раздающим")
	}
	// Второй поиск — с той же сессией, без входа.
	r.Search(ctx, "космос")
	if s.Logins() != 1 {
		t.Fatalf("входов %d после второго поиска", s.Logins())
	}
}

func TestSearchSendsQueryInCP1251(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	if _, err := newRutracker(t, s, withCreds("user", "pass")).Search(ctx, "  космос  "); err != nil {
		t.Fatal(err)
	}
	if s.LastQuery() != "космос" {
		t.Fatalf("сервер получил %q", s.LastQuery())
	}
}

func TestLoginSendsCP1251Form(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "Пользователь", "пароль"
	if err := newRutracker(t, s, withCreds("Пользователь", "пароль")).Login(ctx); err != nil {
		t.Fatalf("вход кириллицей: %v", err)
	}
}

func TestSearchWithoutCredentials(t *testing.T) {
	s := rutrackertest.NewServer(t)
	if _, err := newRutracker(t, s, nil).Search(ctx, "космос"); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("получено %v", err)
	}
	if s.Hits("/forum/tracker.php") != 0 || s.Logins() != 0 {
		t.Fatal("без логина на форум ходить незачем")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := rutrackertest.NewServer(t)
	if _, err := newRutracker(t, s, withCreds("user", "pass")).Search(ctx, "   "); err == nil {
		t.Fatal("ошибки нет")
	}
}

func TestWrongPasswordStopsFurtherLogins(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "right"
	r := newRutracker(t, s, withCreds("user", "wrong"))
	for range 3 {
		if _, err := r.Search(ctx, "космос"); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("получено %v", err)
		}
		r.Details(ctx, "6914565") // страница раздачи тоже не должна пробовать войти
	}
	if s.Logins() != 1 {
		t.Fatalf("попыток входа %d — после первой неудачи Rutracker требует капчу", s.Logins())
	}
	r.SetCredentials("user", "right")
	if _, err := r.Search(ctx, "космос"); err != nil || s.Logins() != 2 {
		t.Fatalf("после смены пароля: %v, входов %d", err, s.Logins())
	}
}

func TestWrongPasswordErrorHidesPassword(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "right"
	err := newRutracker(t, s, withCreds("user", "Секрет-123")).Login(ctx)
	if err == nil || strings.Contains(err.Error(), "Секрет-123") {
		t.Fatalf("ошибка %v", err)
	}
}

func TestCaptchaBlocksAutoLogin(t *testing.T) {
	s := rutrackertest.NewServer(t)
	r := newRutracker(t, s, withCreds("user", "pass"))
	r.mu.Lock()
	r.loginBlock = &CaptchaError{SID: "x"} // как после ответа с капчей
	r.mu.Unlock()
	var ce *CaptchaError
	if err := r.Login(ctx); !errors.As(err, &ce) || s.Logins() != 0 {
		t.Fatalf("получено %v, входов %d", err, s.Logins())
	}
}

func TestDetails(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	d, err := newRutracker(t, s, withCreds("user", "pass")).Details(ctx, "6914565")
	if err != nil {
		t.Fatal(err)
	}
	if d.TopicID != "6914565" || d.Tracker != "rutracker" || d.Seeders != 134 || d.TorrentURL != "" ||
		!strings.HasPrefix(d.Magnet, "magnet:?xt=urn:btih:5fab7552") {
		t.Fatalf("раздача: %+v", d.Release)
	}
	if s.Logins() != 1 {
		t.Fatalf("входов %d: с логином раздача открывается после входа (видны раздающие)", s.Logins())
	}
}

func TestDetailsAsGuestWithoutCredentials(t *testing.T) {
	s := rutrackertest.NewServer(t)
	d, err := newRutracker(t, s, nil).Details(ctx, "6914565")
	if err != nil || d.Title == "" || s.Logins() != 0 {
		t.Fatalf("гостем: %v, входов %d", err, s.Logins())
	}
}

func TestDetailsOfRemovedRelease(t *testing.T) {
	s := rutrackertest.NewServer(t)
	if _, err := newRutracker(t, s, nil).Details(ctx, "99999999"); !errors.Is(err, source.ErrRemoved) {
		t.Fatalf("получено %v", err)
	}
}

func TestDetailsRejectsBadID(t *testing.T) {
	s := rutrackertest.NewServer(t)
	if _, err := newRutracker(t, s, nil).Details(ctx, "1 OR 1"); err == nil || s.Hits("/forum/viewtopic.php") != 0 {
		t.Fatal("номер раздачи не проверен")
	}
}

// Форум закрыт проверкой, а Edge не справился — топы, разделы и лента (API) работают.
func TestAPIWorksWhileForumIsClosed(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	r := newRutracker(t, s, func(o *Options) { o.Passer = &fakePasser{err: errors.New("Edge не прошёл проверку")} })
	if _, err := r.Details(ctx, "6914565"); !errors.Is(err, netx.ErrChallenge) {
		t.Fatalf("раздача: %v", err)
	}
	if _, err := r.Top(ctx, "2076", 10); err != nil {
		t.Fatalf("топ: %v", err)
	}
	if _, err := r.Categories(ctx); err != nil {
		t.Fatalf("разделы: %v", err)
	}
	if _, err := r.Recent(ctx, "313"); err != nil {
		t.Fatalf("лента: %v", err)
	}
}

// Одновременные поиски входят один раз — и с верным паролем, и с неверным (вторая неудачная
// попытка — лишний шаг к капче).
func TestConcurrentSearchesLogInOnce(t *testing.T) {
	for name, pass := range map[string]string{"верный пароль": "right", "неверный пароль": "wrong"} {
		t.Run(name, func(t *testing.T) {
			s := rutrackertest.NewServer(t)
			s.Login, s.Password = "user", "right"
			s.BeforeLogin = func() { time.Sleep(100 * time.Millisecond) } // вход длится — остальные успевают подойти
			r := newRutracker(t, s, withCreds("user", pass))
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() { r.Search(ctx, "космос") })
			}
			wg.Wait()
			if s.Logins() != 1 {
				t.Fatalf("попыток входа %d", s.Logins())
			}
		})
	}
}

// Пароль сменили, пока шёл вход со старым: неудача старого входа не блокирует новый пароль.
func TestPasswordChangeDuringLoginIsKept(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "right"
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.BeforeLogin = func() { once.Do(func() { close(entered); <-release }) }
	r := newRutracker(t, s, withCreds("user", "wrong"))
	done := make(chan error, 1)
	go func() { done <- r.Login(ctx) }()
	<-entered
	r.SetCredentials("user", "right")
	close(release)
	if err := <-done; !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("старый вход: %v", err)
	}
	if err := r.Login(ctx); err != nil {
		t.Fatalf("новый пароль заблокирован неудачей старого входа: %v", err)
	}
}

// Зеркало со «/» на конце — сессия находится, вход один на все поиски (ревью этапа 4).
func TestMirrorWithTrailingSlashKeepsSession(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	r := newRutracker(t, s, func(o *Options) {
		o.Login, o.Password = "user", "pass"
		o.Mirrors = []string{s.Forum.URL + "/"}
	})
	for range 3 {
		if _, err := r.Search(ctx, "космос"); err != nil {
			t.Fatal(err)
		}
	}
	if s.Logins() != 1 {
		t.Fatalf("входов %d — сессия на зеркале со «/» не находится", s.Logins())
	}
}

// Суточное обновление дерева разделов не удалось (API недоступен) — поиск работает по старому
// дереву, а следующая попытка обновления — не раньше чем через 10 минут (ревью этапа 4).
func TestSearchUsesStaleTreeWhenAPIFails(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	r := newRutracker(t, s, withCreds("user", "pass"))
	if _, err := r.Search(ctx, "космос"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.treeAt = r.treeAt.Add(-25 * time.Hour) // дерево устарело
	r.mu.Unlock()
	s.APIDown.Store(true)
	before := s.Hits("/v1/static/cat_forum_tree")
	for range 2 {
		if _, err := r.Search(ctx, "космос"); err != nil {
			t.Fatalf("поиск при недоступном API: %v", err)
		}
	}
	if got := s.Hits("/v1/static/cat_forum_tree") - before; got != 1 {
		t.Fatalf("обновлений дерева %d за два поиска — после неудачи повтор через 10 минут", got)
	}
}

// Сессия истекла на стороне форума, а раздача видна только вошедшим — тихий повторный вход
// (спека, раздел 16), как у поиска.
func TestDetailsLogsInAgainWhenSessionExpired(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	s.TopicNeedsLogin = true
	r := newRutracker(t, s, withCreds("user", "pass"))
	if _, err := r.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	s.ExpireSessions()
	if _, err := r.Details(ctx, "6914565"); err != nil {
		t.Fatalf("после истечения сессии: %v", err)
	}
	if s.Logins() != 2 {
		t.Fatalf("входов %d, нужно 2", s.Logins())
	}
}

// Вход заблокирован (неверный пароль) — каталог открывает сотни раздач, а предупреждение в
// журнале одно.
func TestDetailsWarnsOnceWhileLoginBlocked(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "right"
	var buf bytes.Buffer
	r := newRutracker(t, s, func(o *Options) {
		o.Login, o.Password = "user", "wrong"
		o.Log = slog.New(slog.NewTextHandler(&buf, nil))
	})
	for range 3 {
		if _, err := r.Details(ctx, "6914565"); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "вход не удался"); n != 1 {
		t.Fatalf("предупреждений %d:\n%s", n, buf.String())
	}
}

// Вход сорвался из-за сети — страница раздачи минуту не пробует войти снова: иначе каждая из
// сотен раздач каталога отправляла бы форму входа (ревью этапа 4).
func TestDetailsPausesLoginAfterTransientFailure(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	var broken atomic.Bool
	broken.Store(true)
	s.BeforeLogin = func() {
		if broken.Load() {
			panic(http.ErrAbortHandler) // ответ на вход обрывается
		}
	}
	now := time.Now()
	r := newRutracker(t, s, withCreds("user", "pass"))
	r.now = func() time.Time { return now }
	for range 3 {
		if _, err := r.Details(ctx, "6914565"); err != nil {
			t.Fatal(err) // раздача открывается как для гостя
		}
	}
	if s.Logins() != 1 {
		t.Fatalf("попыток входа %d за минуту", s.Logins())
	}
	broken.Store(false)
	now = now.Add(2 * time.Minute)
	if _, err := r.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	if s.Logins() != 2 || !r.loggedIn() {
		t.Fatalf("через минуту: входов %d, вошли %v", s.Logins(), r.loggedIn())
	}
}

// search-raw: параметры tracker.php как есть (для проверки f= вживую), значения — в windows-1251,
// без фильтра по категориям поиска.
func TestSearchRaw(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	r := newRutracker(t, s, withCreds("user", "pass"))
	rs, err := r.SearchRaw(ctx, url.Values{"nm": {"космос"}, "f": {"2076"}})
	if err != nil || len(rs) != 50 {
		t.Fatalf("найдено %d, %v", len(rs), err)
	}
	if s.LastQuery() != "космос" || s.LastForums() != "2076" {
		t.Fatalf("сервер получил nm=%q f=%q", s.LastQuery(), s.LastForums())
	}
}
