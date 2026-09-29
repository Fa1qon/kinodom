// Package labels — метки каналов: категория, страна, языки (спека этапа 8, раздел 5.4). Основной
// источник — открытая база iptv-org, запасной путь — id телепрограммы, пометки в названии и слова
// в группах плейлистов.
package labels

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"kinodom/internal/iptv/m3u"
)

// OrgChannel — канал iptv-org: только то, что нужно для меток и моста.
type OrgChannel struct {
	ID         string
	Name       string
	AltNames   []string
	Country    string
	Categories []string
	NSFW       bool
	Languages  []string // языки основной ленты
}

// Base — база iptv-org в памяти.
type Base struct {
	byID   map[string]*OrgChannel
	byName map[string][]*OrgChannel // нормализованное название или синоним → каналы
}

// LoadBase читает channels.json и feeds.json iptv-org.
func LoadBase(channels, feeds io.Reader) (*Base, error) {
	var cs []struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		AltNames   []string `json:"alt_names"`
		Country    string   `json:"country"`
		Categories []string `json:"categories"`
		NSFW       bool     `json:"is_nsfw"`
	}
	if err := json.NewDecoder(channels).Decode(&cs); err != nil {
		return nil, fmt.Errorf("база iptv-org, каналы: %w", err)
	}
	var fs []struct {
		Channel   string   `json:"channel"`
		Main      bool     `json:"is_main"`
		Languages []string `json:"languages"`
	}
	if err := json.NewDecoder(feeds).Decode(&fs); err != nil {
		return nil, fmt.Errorf("база iptv-org, ленты: %w", err)
	}
	b := &Base{byID: make(map[string]*OrgChannel, len(cs)), byName: map[string][]*OrgChannel{}}
	for _, c := range cs {
		oc := &OrgChannel{ID: c.ID, Name: c.Name, AltNames: c.AltNames, Country: c.Country, Categories: c.Categories, NSFW: c.NSFW}
		b.byID[c.ID] = oc
		seen := map[string]bool{}
		for _, n := range append([]string{c.Name}, c.AltNames...) {
			k := m3u.Norm(n)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			b.byName[k] = append(b.byName[k], oc)
		}
	}
	for _, f := range fs {
		if c := b.byID[f.Channel]; c != nil && f.Main && len(c.Languages) == 0 {
			c.Languages = f.Languages
		}
	}
	return b, nil
}

// ByID — канал iptv-org по id; nil — нет.
func (b *Base) ByID(id string) *OrgChannel {
	if b == nil {
		return nil
	}
	return b.byID[id]
}

// Names — названия канала iptv-org для моста (раздел 5.3): название и синонимы.
func (c *OrgChannel) Names() []string { return append([]string{c.Name}, c.AltNames...) }

// Find — канал iptv-org: по id из tvg-id потоков канала — тот, что встречается чаще других; иначе
// по названиям канала телепрограммы, если подходит ровно один канал. nil — не найден.
func (b *Base) Find(ids []string, names []string) *OrgChannel {
	if b == nil {
		return nil
	}
	// По tvg-id — большинством: в плейлистах бывает поток, подписанный чужим id.
	votes := map[*OrgChannel]int{}
	for _, id := range ids {
		if c := b.byID[id]; c != nil {
			votes[c]++
		}
	}
	var byID *OrgChannel
	top, tie := 0, false
	for c, n := range votes {
		switch {
		case n > top:
			byID, top, tie = c, n, false
		case n == top:
			tie = true
		}
	}
	if byID != nil && !tie {
		return byID
	}
	var found *OrgChannel
	for _, n := range names {
		cs := b.byName[m3u.Norm(n)]
		switch {
		case len(cs) == 0:
			continue
		case len(cs) > 1:
			return nil
		case found == nil:
			found = cs[0]
		case found != cs[0]:
			return nil
		}
	}
	return found
}

// Source — метки из одного источника; пустое поле — метки там нет.
type Source struct {
	Category  string
	Country   string
	Languages []string
}

// Labels — итоговые метки канала. Category "" — «Без категории», Country "" — «Страна не указана»,
// Languages пусто — «Язык не указан».
type Labels struct {
	Category  string   `json:"category"`
	Country   string   `json:"country"`
	Languages []string `json:"languages"`
}

// Merge — метка из первого источника, где она есть; страна Россия без языка — русский.
func Merge(sources ...Source) Labels {
	l := Labels{Languages: []string{}}
	for _, s := range sources {
		if l.Category == "" {
			l.Category = s.Category
		}
		if l.Country == "" {
			l.Country = s.Country
		}
		if len(l.Languages) == 0 && len(s.Languages) > 0 {
			l.Languages = slices.Clone(s.Languages)
		}
	}
	if len(l.Languages) == 0 && l.Country == "RU" {
		l.Languages = []string{"rus"}
	}
	return l
}

// Категории — постоянный набор в порядке экрана; "" — «Без категории».
var CategoryOrder = []string{"movies", "sports", "science", "news", "kids", "music", "entertainment", "general", "religious", "adult", ""}

var categoryNames = map[string]string{
	"movies": "Фильмы и сериалы", "sports": "Спорт", "science": "Познавательные", "news": "Новости",
	"kids": "Детские", "music": "Музыка", "entertainment": "Развлекательные", "general": "Общие",
	"religious": "Религия", "adult": "18+", "": "Без категории",
}

// orgCategories — категории iptv-org → наши (спека, раздел 5.4).
var orgCategories = map[string]string{
	"movies": "movies", "series": "movies", "classic": "movies",
	"sports":      "sports",
	"documentary": "science", "science": "science", "education": "science", "travel": "science", "outdoor": "science", "culture": "science",
	"news": "news", "business": "news", "weather": "news", "legislative": "news",
	"kids": "kids", "animation": "kids",
	"music":         "music",
	"entertainment": "entertainment", "comedy": "entertainment", "lifestyle": "entertainment", "cooking": "entertainment",
	"auto": "entertainment", "relax": "entertainment", "shop": "entertainment", "interactive": "entertainment",
	"family":  "entertainment", // у iptv-org «family» — семейные развлекательные (СТС); детские помечены kids
	"general": "general", "public": "general",
	"religious": "religious",
	"xxx":       "adult",
}

// seniority — старшинство, когда категории iptv-org ведут в разные наши: чем раньше, тем важнее.
var seniority = []string{"adult", "kids", "sports", "news", "movies", "science", "music", "religious", "entertainment", "general"}

// FromOrg — метки канала iptv-org.
func FromOrg(c *OrgChannel) Source {
	if c == nil {
		return Source{}
	}
	s := Source{Country: c.Country, Languages: c.Languages}
	if s.Country == "UK" {
		s.Country = "GB"
	}
	if c.NSFW {
		s.Category = "adult"
		return s
	}
	best := len(seniority)
	for _, oc := range c.Categories {
		if i := slices.Index(seniority, orgCategories[oc]); i >= 0 && i < best {
			best = i
		}
	}
	if best < len(seniority) {
		s.Category = seniority[best]
	}
	return s
}

var (
	reSuffix = regexp.MustCompile(`-([a-z]{2})$`)
	reTag    = regexp.MustCompile(`\[([A-Z]{2})\]`)
)

// FromID — страна по двухбуквенному коду в конце id телепрограммы («tet-ua») или пометке «[BG]» в
// названии. Суффикс «-plN» региональной версии — не страна.
func FromID(id string, names []string) Source {
	if m := reSuffix.FindStringSubmatch(id); m != nil {
		if c := strings.ToUpper(m[1]); knownCountry(c) {
			return Source{Country: c}
		}
	}
	for _, n := range names {
		if m := reTag.FindStringSubmatch(n); m != nil && knownCountry(m[1]) {
			return Source{Country: m[1]}
		}
	}
	return Source{}
}

// idCountries — коды стран, которые iptvx.one пишет в конце id («tet-ua»). Список явный: «-tv» в
// «match-tv» — не Тувалу (исследование, раздел 18).
var idCountries = map[string]bool{
	"UA": true, "BY": true, "KZ": true, "MD": true, "BG": true, "RU": true, "UZ": true, "AM": true, "GE": true,
	"AZ": true, "LV": true, "LT": true, "EE": true, "TR": true, "DE": true, "FR": true, "PL": true, "IT": true,
	"ES": true, "RS": true, "HR": true, "RO": true, "CZ": true, "IL": true, "KG": true, "TJ": true, "US": true,
}

func knownCountry(code string) bool { return idCountries[code] }

type rule struct {
	value string
	re    *regexp.Regexp
}

// Слова групп плейлистов. Флажки не используются: в группах они обозначают страну VPN.
var (
	groupCategories = []rule{
		{"adult", regexp.MustCompile(`18\+|взросл|adult|xxx`)},
		{"movies", regexp.MustCompile(`кино|фильм|сериал|movie|film|cinema|series`)},
		{"sports", regexp.MustCompile(`спорт|sport|футбол|football`)},
		{"science", regexp.MustCompile(`позна|научн|документ|discovery|history|nature|природ|educat|обучен`)},
		{"news", regexp.MustCompile(`новост|news|информ`)},
		{"kids", regexp.MustCompile(`дет|мульт|kids|cartoon|children`)},
		{"music", regexp.MustCompile(`муз|music`)},
		{"entertainment", regexp.MustCompile(`развлеч|развлек|entertain|юмор|comedy|lifestyle|кулинар`)},
		{"religious", regexp.MustCompile(`религ|relig`)},
	}
	groupCountries = []rule{
		{"UA", regexp.MustCompile(`украин|ukrain`)},
		{"BY", regexp.MustCompile(`беларус|белорус|belarus`)},
		{"KZ", regexp.MustCompile(`казах|kazakh`)},
		{"US", regexp.MustCompile(`сша|\busa\b`)},
		{"GB", regexp.MustCompile(`великобр|britain`)},
		{"DE", regexp.MustCompile(`герман|german|deutsch`)},
		{"FR", regexp.MustCompile(`франц|france|french`)},
		{"TR", regexp.MustCompile(`турц|turk`)},
		{"PL", regexp.MustCompile(`польш|poland|polsk`)},
		{"RU", regexp.MustCompile(`росси|russia|федерал|регион|местн`)},
	}
)

// FromGroups — категория и страна по словам в группах потоков канала: у каждой группы — первая
// подходящая, итог — самая частая по числу потоков.
func FromGroups(groups map[string]int) Source {
	return Source{Category: vote(groups, groupCategories), Country: vote(groups, groupCountries)}
}

func vote(groups map[string]int, rules []rule) string {
	tally := map[string]int{}
	for g, n := range groups {
		lg := strings.ToLower(g)
		for _, r := range rules {
			if r.re.MatchString(lg) {
				tally[r.value] += n
				break
			}
		}
	}
	best, bestN := "", 0
	for _, r := range rules { // при равенстве — порядок правил
		if n := tally[r.value]; n > bestN {
			best, bestN = r.value, n
		}
	}
	return best
}

// CategoryName — название категории.
func CategoryName(id string) string {
	if n, ok := categoryNames[id]; ok {
		return n
	}
	return id
}

var (
	regionNames   = display.Russian.Regions()
	languageNames = display.Russian.Languages()
)

// CountryName — название страны по-русски; "" — «Страна не указана».
func CountryName(code string) string {
	switch code {
	case "":
		return "Страна не указана"
	case "US":
		return "США"
	}
	r, err := language.ParseRegion(code)
	if err != nil || !r.IsCountry() {
		return code
	}
	if n := regionNames.Name(r); n != "" {
		return n
	}
	return code
}

// LanguageName — название языка по-русски (код iptv-org из трёх букв); "" — «Язык не указан».
func LanguageName(code string) string {
	if code == "" {
		return "Язык не указан"
	}
	t, err := language.Parse(code)
	if err != nil {
		return code
	}
	if n := languageNames.Name(t); n != "" {
		return n
	}
	return code
}
