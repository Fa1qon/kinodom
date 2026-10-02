package web

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// required — файлы пульта, без которых он не работает (спека этапа 7, раздел 6.1).
var required = []string{
	"index.html", "style.css", "app.js", "api.js", "ui.js", "icons.js", "nav.js",
	"favicon.ico", "icon-192.png", "logo-icon.png", "logo-text.png",
	"fonts/golos-text-cyrillic.woff2", "fonts/golos-text-latin.woff2",
	"fonts/unbounded-cyrillic.woff2", "fonts/unbounded-latin.woff2",
	"fonts/OFL-golos-text.txt", "fonts/OFL-unbounded.txt",
	"views/catalog.js", "views/release.js", "views/search.js", "views/downloads.js",
	"views/settings-layout.js", "views/settings-status.js", "views/settings-params.js", "views/settings-sections.js", "views/updates.js",
	"views/channels.js", "views/channel.js", "views/channel-settings.js", "views/tvkit.js", "views/settings-iptv.js", "views/settings-unrecognized.js",
	"views/history.js", "views/library.js", "views/library-card.js", "views/library-parts.js", "views/catalog-parts.js", "views/settings-library.js", "views/folders.js", "views/setup.js",
}

// scripts — все модули пульта.
func scripts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(Static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		b, err := fs.ReadFile(Static, p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Пульт встроен в exe целиком: каждый нужный файл на месте, страница подключает стиль и главный
// модуль.
func TestPultFilesEmbedded(t *testing.T) {
	for _, f := range required {
		if _, err := fs.Stat(Static, f); err != nil {
			t.Errorf("нет %s", f)
		}
	}
	index, err := fs.ReadFile(Static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(index, []byte(`<script type="module" src="app.js">`)) || !bytes.Contains(index, []byte(`href="style.css"`)) {
		t.Errorf("index.html не подключает app.js как модуль или style.css:\n%s", index)
	}
}

var reImport = regexp.MustCompile(`(?:from|import)\s*\(?\s*'(\.[^']+)'`)

// Каждый импорт модуля ведёт к встроенному файлу: опечатка в пути — белый экран без ошибки на
// сервере.
func TestPultImportsResolve(t *testing.T) {
	for file, src := range scripts(t) {
		for _, m := range reImport.FindAllStringSubmatch(src, -1) {
			target := path.Join(path.Dir(file), m[1])
			if _, err := fs.Stat(Static, target); err != nil {
				t.Errorf("%s: импорт %s — файла %s нет", file, m[1], target)
			}
		}
	}
}

var (
	reIconUse = regexp.MustCompile(`icon\('([a-z_]+)'`)
	reIconDef = regexp.MustCompile(`(?m)^\s+([a-z_]+): '`)
)

// Иконки, которые зовут экраны, есть в icons.js: иначе кнопка без значка.
func TestPultIconsExist(t *testing.T) {
	src := scripts(t)
	have := map[string]bool{}
	for _, m := range reIconDef.FindAllStringSubmatch(src["icons.js"], -1) {
		have[m[1]] = true
	}
	for file, s := range src {
		for _, m := range reIconUse.FindAllStringSubmatch(s, -1) {
			if !have[m[1]] {
				t.Errorf("%s: иконки %q нет в icons.js", file, m[1])
			}
		}
	}
}

// lookNode — Node на этом ПК; без него проверка JavaScript пропускается.
func lookNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node не установлен — JavaScript пульта не проверяется")
	}
	return node
}

// Модули разбираются как JavaScript: синтаксическая ошибка в модуле — белый экран, а серверные
// тесты её не видят.
func TestPultModulesParse(t *testing.T) {
	node := lookNode(t)
	for file, src := range scripts(t) {
		cmd := exec.Command(node, "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", file, err, out)
		}
	}
}

// Числа и имена в пульте — по-русски и коротко: размеры, скорость, склонение, имена серий без
// общего начала и конца.
func TestPultFormatting(t *testing.T) {
	node := lookNode(t)
	script := `
import { size, speed, plural, shortNames, fileFormat } from './ui.js';
const checks = [
  [size(18683035238), '17,4 ГБ'], [size(1034944512), '987 МБ'], [size(0), '0 МБ'],
  [speed(3250586), '3,1 МБ/с'], [speed(870400), '850 КБ/с'],
  [plural(1, 'пир', 'пира', 'пиров'), '1 пир'], [plural(3, 'пир', 'пира', 'пиров'), '3 пира'],
  [plural(12, 'пир', 'пира', 'пиров'), '12 пиров'], [plural(22, 'пир', 'пира', 'пиров'), '22 пира'],
  [shortNames(['The.Dinosaurs.S01E01.720p.NF.WEB-DL.mkv', 'The.Dinosaurs.S01E02.720p.NF.WEB-DL.mkv']).join('|'), 'S01E01|S01E02'],
  [shortNames(['Сезон 1/01. Начало.mkv', 'Сезон 1/02. Финал.mkv']).join('|'), '01 Начало|02 Финал'],
  [shortNames(['film.mkv']).join('|'), 'film'],
  [shortNames(['a.mkv', 'a.mkv']).join('|'), 'a|a'],
  [fileFormat('Сезон 1/01. Начало.mkv'), 'MKV'], [fileFormat('film.m4v'), 'M4V'], [fileFormat('Сезон\\01.avi'), 'AVI'], [fileFormat('README'), ''],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Загрузки» по раздачам: строки файлов собираются в раздачи в порядке первой строки, серии — по имени, как
// на экране раздачи (номер файла в торренте бывает не по порядку серий);
// общее состояние — первое, что есть: смотрят, качается, на паузе, в очереди, скачано; процент — от
// общего размера; удалить раздачу можно, если хоть одну её серию не смотрят (спека этапа 7, раздел 10.7).
func TestPultDownloadGroups(t *testing.T) {
	node := lookNode(t)
	script := `
import { groupDownloads, groupLine } from './views/downloads.js';
const f = (hash, index, state, size, done, extra = {}) =>
  ({ hash, index, state, size, done, file: hash + index + '.mkv', release: null, canDelete: true, ...extra });
const gs = groupDownloads([
  f('a', 2, 'downloading', 100, 50, { speed: 10 }),
  f('b', 0, 'done', 300, 300),
  f('a', 0, 'done', 100, 100),
  f('a', 1, 'queued', 100, 0),
  f('c', 0, 'paused', 200, 20, { canDelete: false }),
  f('c', 1, 'watching', 200, 0, { canDelete: false }),
  f('d', 0, 'done', 10, 10, { file: 'S01E10.mkv' }),
  f('d', 1, 'done', 10, 10, { file: 'S01E02.mkv' }),
  f('d', 2, 'done', 10, 10, { file: 'S01E01.mkv' }),
]);
const got = gs.map((g) => [g.hash, g.items.map((d) => d.index).join(''), g.state, g.size, g.percent, g.speed, g.canDelete].join(' ')).join(' | ');
const want = 'a 012 downloading 300 50 10 true | b 0 done 300 100 0 true | c 01 watching 400 5 0 false | d 210 done 30 100 0 true';
if (got !== want) {
  console.error(got, '≠', want);
  process.exitCode = 1;
}
// Вторая строка раздачи различает сезоны и качество одного сериала; у фильма из одного файла — файл.
const lines = [
  [groupLine({ release: { season: 'S01', quality: 'WEB-DL 1080p' }, items: [f('x', 0, 'done', 1, 1), f('x', 1, 'done', 1, 1)] }), 'S01 · WEB-DL 1080p · 2 серии'],
  [groupLine({ release: { season: '', quality: 'BDRip' }, items: [f('y', 0, 'done', 1, 1, { file: 'film.mkv' })] }), 'BDRip · film.mkv'],
  [groupLine({ release: null, items: [f('z', 0, 'done', 1, 1), f('z', 1, 'done', 1, 1), f('z', 2, 'done', 1, 1), f('z', 3, 'done', 1, 1), f('z', 4, 'done', 1, 1)] }), '5 серий'],
];
for (const [got, want] of lines) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Каналы» (спека этапа 8, раздел 6.2): вкладка «Избранные» — только избранное устройства; вкладка
// категории — все каналы категории, в том числе избранные и федеральные; страна и язык — в том числе
// «не указаны»; во «Всех» — заголовки «Избранные», «Федеральные» и категории по порядку сервера.
func TestPultChannelsFilter(t *testing.T) {
	node := lookNode(t)
	script := `
import { filterChannels, sections, UNKNOWN, filtersFrom } from './views/channels.js';
import { progressOf, hhmm } from './views/tvkit.js';
import { dateStr } from './views/channel.js';
import { labelPatch, sourceMarks, sourceButtons, sourceGrade } from './views/channel-settings.js';
const c = (key, block, category, categoryName, country, languages) => ({ key, block, category, categoryName, country, languages });
const all = [
  c('bbc', 'favorite', 'news', 'Новости', 'GB', ['eng']),
  c('pervy', 'federal', 'general', 'Общие', 'RU', ['rus']),
  c('match', 'federal', 'sports', 'Спорт', 'RU', ['rus']),
  c('kino', '', 'movies', 'Фильмы и сериалы', 'RU', ['rus']),
  c('euro', '', 'sports', 'Спорт', 'FR', ['fra', 'eng']),
  c('local', '', '', 'Без категории', '', []),
];
const keys = (cs) => cs.map((x) => x.key).join(' ');
const checks = [
  [keys(filterChannels(all, { tab: 'all' })), 'bbc pervy match kino euro local'],
  [keys(filterChannels(all, { tab: 'fav' })), 'bbc'],
  [keys(filterChannels(all, { tab: 'sports' })), 'match euro'],
  [keys(filterChannels(all, { tab: 'all', country: 'RU' })), 'pervy match kino'],
  [keys(filterChannels(all, { tab: 'all', country: UNKNOWN })), 'local'],
  [keys(filterChannels(all, { tab: 'all', lang: 'eng' })), 'bbc euro'],
  [keys(filterChannels(all, { tab: 'all', lang: UNKNOWN })), 'local'],
  [keys(filterChannels(all, { tab: 'sports', lang: 'fra' })), 'euro'],
  // «Федеральные» — каналы с номером кнопки, в том числе из избранного (отзыв заказчика 2026-09-30).
  [keys(filterChannels(all.map((x) => ({ ...x, number: { pervy: 1, match: 3, bbc: 0 }[x.key] || 0 })), { tab: 'federal' })), 'pervy match'],
  // Фильтры: из адреса, а без параметра — запомненные; «Все» в адресе сильнее запомненной вкладки.
  [JSON.stringify(filtersFrom(new URLSearchParams('tab=all&country=&lang='), { tab: 'science', country: 'RU', lang: 'rus' })), '{"tab":"all","country":"","lang":""}'],
  [JSON.stringify(filtersFrom(new URLSearchParams(''), { tab: 'science', country: 'RU' })), '{"tab":"science","country":"RU","lang":""}'],
  [JSON.stringify(filtersFrom(new URLSearchParams(''), {})), '{"tab":"all","country":"","lang":""}'],
  [sections(all, 'all').map((s) => s.title + ':' + s.items.length).join(' | '), 'Избранные:1 | Федеральные:2 | Фильмы и сериалы:1 | Спорт:1 | Без категории:1'],
  [sections(filterChannels(all, { tab: 'sports' }), 'sports').map((s) => s.title + ':' + s.items.length).join(' | '), ':2'],
  [sections([], 'sports').length, 0],
  [progressOf({ start: '2026-09-29T19:00:00+07:00', stop: '2026-09-29T20:00:00+07:00' }, Date.parse('2026-09-29T19:15:00+07:00')), 25],
  [progressOf({ start: '2026-09-29T19:00:00+07:00', stop: '2026-09-29T20:00:00+07:00' }, Date.parse('2026-09-29T21:00:00+07:00')), 100],
  // Время и день программы — по поясу каналов из настроек, а не по часам устройства (отзыв заказчика
  // 2026-09-30: на ПК московское время, в настройках UTC+7).
  [hhmm('2026-09-29T16:00:00Z', 7), '23:00'],
  [hhmm('2026-09-29T16:00:00Z', 3), '19:00'],
  [hhmm('2026-09-29T20:30:00+03:00', 7), '00:30'],
  [dateStr(1, 7, new Date(Date.UTC(2026, 8, 30, 17, 30))), '2026-10-02'],
  [dateStr(1, 3, new Date(Date.UTC(2026, 8, 30, 17, 30))), '2026-10-01'],
  // Источники в настройках канала: «основной», «без звука», «скрыт»; «Сделать основным», «Скрыть»,
  // «Вернуть» (отзыв заказчика 2026-09-30).
  [sourceMarks({ offered: true, audio: true }, 0).join(), 'основной'],
  [sourceMarks({ offered: true, pinned: true, audio: false }, 0).join(), 'основной — выбран вручную,без звука'],
  [sourceMarks({ offered: true, audio: null }, 1).join(), ''],
  [sourceMarks({ offered: false, hidden: true }, 3).join(), 'скрыт'],
  // Скрытый, но других рабочих нет — плеер получит его запасным (финальное ревью).
  [sourceMarks({ offered: true, hidden: true, audio: false }, 0).join(), 'скрыт, но других рабочих нет — плеер получит его,без звука'],
  [sourceButtons({ offered: true, hidden: true }, 0, [{ offered: true, hidden: true }]).join(), 'show'],
  [sourceButtons({ offered: true }, 0, [{ offered: true }, { offered: true }]).join(), 'keep,hide,other'],
  // Последний источник версии, а у канала есть другие рабочие версии (11b-Е): «Скрыть» доступна.
  [sourceButtons({ offered: true }, 0, [{ offered: true }], true).join(), 'keep,hide,other'],
  [sourceButtons({ offered: true }, 0, [{ offered: true }]).join(), 'keep,hide-last,other'],
  [sourceButtons({ offered: true }, 1, [{ offered: true }, { offered: true }]).join(), 'main,hide,other'],
  [sourceButtons({ offered: true, pinned: true }, 0, [{ offered: true }, { offered: true }]).join(), 'unpin,hide,other'],
  [sourceButtons({ offered: true }, 0, [{ offered: true }, { offered: false }]).join(), 'keep,hide-last,other'],
  [sourceButtons({ offered: false }, 1, [{ offered: true }, { offered: false }]).join(), 'hide,other'],
  [sourceButtons({ offered: false, hidden: true }, 1, [{ offered: true }, { offered: false, hidden: true }]).join(), 'show'],
  // Правка меток — только изменённые поля (финальное ревью этапа 8); языки — списком (хвост Х31).
  [JSON.stringify(labelPatch({ category: 'news', country: 'RU', languages: ['rus'] }, { category: 'news', country: 'RU', langs: ['rus'] })), '{}'],
  [JSON.stringify(labelPatch({ category: 'news', country: 'RU', languages: ['rus'] }, { category: '', country: 'RU', langs: ['rus'] })), '{"category":""}'],
  [JSON.stringify(labelPatch({ category: 'news', country: 'RU', languages: [] }, { category: 'news', country: 'UA', langs: ['ukr'] })), '{"country":"UA","languages":["ukr"]}'],
  [JSON.stringify(labelPatch({ category: 'news', country: 'RU', languages: ['rus', 'eng'] }, { category: 'news', country: 'RU', langs: [] })), '{"languages":[]}'],
  [JSON.stringify(labelPatch({ category: 'news', country: 'RU', languages: ['rus', 'eng'] }, { category: 'news', country: 'RU', langs: ['eng', 'rus'] })), '{}'],
  [JSON.stringify(labelPatch({ category: 'news', country: 'RU', languages: ['rus'] }, { category: 'news', country: 'RU', langs: ['rus', 'eng'] })), '{"languages":["rus","eng"]}'],
  // Скрытый вручную источник, который плееру не предлагается, — «скрыт», а не ⚫ «не отвечает» (Х32).
  [sourceGrade({ offered: false, hidden: true, state: 'alive' }), 'hidden'],
  [sourceGrade({ offered: false, state: 'silent' }), 'black'],
  [sourceGrade({ offered: true, grade: 'green' }), 'green'],
  [sourceGrade({ offered: true, hidden: true, grade: 'yellow' }), 'yellow'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Смотреть» в приложении (план 13b, задача 2; спека 13, 5.2): встроенный плеер — только у приложения с
// playChannels и не выбранным системным плеером; пульт в браузере и приложение 13a — как раньше (Review Focus 1).
// Список плееру — каналы экрана по порядку, стартовый — по ключу версии; подпись версии — только если их несколько.
func TestPultWatchInApp(t *testing.T) {
	node := lookNode(t)
	script := `
import { watchRoute, channelPayload, listFor } from './views/tvkit.js';
const fn = () => true;
const ch = (key, version, extra = {}) => ({ key, version, name: key.toUpperCase(), number: 0, logo: '', versionLabel: 'МСК', versionCount: 1, ...extra });
const list = [ch('pervy', 'pervy-pl4', { number: 1, logo: '/logo/pervy-pl4', versionLabel: 'МСК+4', versionCount: 3 }), ch('ntv', 'ntv', { number: 4 }), ch('kino', 'kino')];
const p1 = channelPayload(list, list[1], 'Федеральные');
const p2 = channelPayload(list, { ...list[0], version: 'pervy-mn1', versionLabel: 'МСК−1' }, 'Все');
const p3 = channelPayload(list, ch('bbc', 'bbc'), 'Избранные');
const stored = { listName: 'Спорт', list: [ch('match', 'match'), ch('euro', 'euro')] };
const checks = [
  [watchRoute(null), 'external'],
  [watchRoute(undefined), 'external'],
  [watchRoute({ version: () => '0.11.0' }), 'external'],
  [watchRoute({ playChannels: fn, player: () => 'system' }), 'external'],
  [watchRoute({ playChannels: fn, player: () => 'builtin' }), 'app'],
  [watchRoute({ playChannels: fn }), 'app'],
  [JSON.stringify(p1), JSON.stringify({ list: [
    { key: 'pervy', version: 'pervy-pl4', name: 'PERVY', number: 1, logo: '/logo/pervy-pl4', label: 'МСК+4' },
    { key: 'ntv', version: 'ntv', name: 'NTV', number: 4, logo: '', label: '' },
    { key: 'kino', version: 'kino', name: 'KINO', number: 0, logo: '', label: '' }], start: 1, listName: 'Федеральные' })],
  // Выбранная на странице версия заменяет версию по умолчанию на её месте в списке.
  [p2.start + ' ' + p2.list[0].version + ' ' + p2.list[0].label + ' ' + p2.list.length, '0 pervy-mn1 МСК−1 3'],
  // Канала нет в списке — он первым.
  [p3.start + ' ' + p3.list.map((x) => x.key).join(','), '0 bbc,pervy,ntv,kino'],
  [JSON.stringify(channelPayload([{ key: 'x' }], { key: 'x' }, 'Все').list[0]), '{"key":"x","version":"x","name":"","number":0,"logo":"","label":""}'],
  [listFor('euro', stored) && listFor('euro', stored).listName, 'Спорт'],
  [listFor('pervy', stored), null],
  [listFor('pervy', null), null],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Настройки → Приложение» (план 13b, задача 3; спека 13, 5.2): пункт есть только в приложении, последним.
func TestPultSettingsApp(t *testing.T) {
	node := lookNode(t)
	script := `
import { settingsItems } from './views/settings-layout.js';
const ids = (xs) => xs.map((x) => x[0]).join(' ');
const checks = [
  [ids(settingsItems(false)), 'status params sections iptv library'],
  [ids(settingsItems(true)), 'status params sections iptv library app'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// История (спека этапа 8, раздел 7.5): где остановились — минутами, процентом или «досмотрено»; какой
// файл продолжать — начатый и недосмотренный, смотренный последним, иначе следующий после последнего
// просмотренного.
func TestPultHistory(t *testing.T) {
	node := lookNode(t)
	script := `
import { duration, whereStopped, resumeIndex } from './views/history.js';
const p = (index, fraction, watched, at, positionSec = 0, durationSec = 0) => ({ index, fraction, watched, updatedAt: at, positionSec, durationSec });
const checks = [
  [duration(3120), '52 мин'], [duration(7080), '1 ч 58 мин'], [duration(3600), '1 ч'],
  [whereStopped(p(0, 0.44, false, '', 3120, 7080)), 'остановились на 52 мин из 1 ч 58 мин'],
  [whereStopped(p(0, 0.43, false, '')), 'остановились на 43 %'],
  [whereStopped(p(0, 0.95, true, '')), 'досмотрено'],
  [whereStopped(null), ''],
  // серии в порядке экрана: 3, 1, 2 (номера файлов в раздаче не по порядку)
  [resumeIndex([3, 1, 2], []), null],
  [resumeIndex([3, 1, 2], [p(3, 1, true, '2026-09-29T10:00:00Z'), p(1, 0.4, false, '2026-09-29T11:00:00Z')]), 1],
  [resumeIndex([3, 1, 2], [p(3, 1, true, '2026-09-29T10:00:00Z')]), 1],
  [resumeIndex([3, 1, 2], [p(3, 1, true, '2026-09-29T10:00:00Z'), p(1, 1, true, '2026-09-29T11:00:00Z')]), 2],
  [resumeIndex([3, 1, 2], [p(2, 1, true, '2026-09-29T12:00:00Z')]), null],
  [resumeIndex([3, 1, 2], [p(1, 0.2, false, '2026-09-29T09:00:00Z'), p(3, 1, true, '2026-09-29T12:00:00Z')]), 1],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Смотреть» на этом ПК: ссылка kinodom://; если браузер за время ожидания не отдал фокус плееру
// (обработчик ссылки не установлен) — запасной адрес .m3u8 (отзыв заказчика 2026-09-30).
func TestPultOpenPlayer(t *testing.T) {
	node := lookNode(t)
	script := `
import { openPlayer } from './ui.js';
const env = () => {
  const e = { listeners: {}, loc: { href: '' }, doc: { hidden: false }, timers: [] };
  e.win = { addEventListener: (n, f) => { e.listeners[n] = f; }, removeEventListener: (n) => { delete e.listeners[n]; } };
  e.wait = (f) => e.timers.push(f);
  return e;
};
const a = env();
openPlayer('kinodom://play?x', '/m3u/a.m3u8', undefined, a);
a.timers.forEach((f) => f());
const b = env();
openPlayer('kinodom://play?x', '/m3u/a.m3u8', undefined, b);
b.listeners.blur();
b.timers.forEach((f) => f());
// Сервер знает, зарегистрирован ли обработчик (хвост Х33): есть — только kinodom://, без таймера;
// нет — сразу .m3u8.
const c = env();
openPlayer('kinodom://play?x', '/m3u/a.m3u8', true, c);
const d = env();
openPlayer('kinodom://play?x', '/m3u/a.m3u8', false, d);
const checks = [
  [a.loc.href, '/m3u/a.m3u8'], // плеер не открылся — скачать .m3u8
  [b.loc.href, 'kinodom://play?x'], // плеер забрал фокус — ничего больше
  [c.loc.href + ' ' + c.timers.length, 'kinodom://play?x 0'],
  [d.loc.href + ' ' + d.timers.length, '/m3u/a.m3u8 0'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Пульт телевизора: стрелка переводит фокус на ближайший элемент в эту сторону; элемент на одной
// линии важнее более близкого наискосок; в сторону, где ничего нет, фокус не уходит; влево и
// вправо — только в своём ряду (с единственного раздела вправо — не в сетку наискосок).
func TestPultSpatialNav(t *testing.T) {
	node := lookNode(t)
	script := `
import { pick } from './nav.js';
const r = (left, top, w = 100, hh = 150) => ({ left, top, right: left + w, bottom: top + hh });
// Шапка: логотип, «Каталог», поиск над правыми карточками; под ней сетка 3×2 карточек.
const logo = r(0, 0, 80, 40), menu = r(120, 0, 80, 40), search = r(260, 0, 200, 40);
const grid = [r(0, 100), r(120, 100), r(240, 100), r(0, 280), r(120, 280), r(240, 280)];
const chip = r(0, 60, 90, 30); // единственный раздел над сеткой
const all = [logo, menu, search, chip, ...grid];
const at = (from, dir) => { const i = pick(from, all.filter((x) => x !== from), dir); return i < 0 ? -1 : all.indexOf(all.filter((x) => x !== from)[i]); };
const checks = [
  [at(grid[0], 'right'), all.indexOf(grid[1])],
  [at(grid[0], 'down'), all.indexOf(grid[3])],
  [at(grid[4], 'up'), all.indexOf(grid[1])],
  [at(grid[2], 'right'), -1],
  [at(grid[5], 'down'), -1],
  [at(grid[0], 'up'), all.indexOf(chip)],
  [at(chip, 'up'), all.indexOf(logo)],
  [at(grid[2], 'up'), all.indexOf(search)],
  [at(logo, 'right'), all.indexOf(menu)],
  [at(grid[3], 'left'), -1],
  [at(chip, 'right'), -1],
  [at(chip, 'down'), all.indexOf(grid[0])],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error('получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Колонки экрана (спека 11b, 4.1): стрелка вправо из строки основной колонки, когда в своём ряду справа
// ничего нет, — в соседнюю колонку (боковую панель раздачи) на её главную кнопку; влево из панели —
// обратно на строку, с которой пришли; колонка без элементов пропускается; на узком экране (панель под
// основной колонкой) прыжка вбок нет.
func TestPultColumnJump(t *testing.T) {
	node := lookNode(t)
	script := `
import { nextColumn, enterColumn } from './nav.js';
const r = (left, top, w, hh) => ({ left, top, right: left + w, bottom: top + hh });
const cover = r(0, 100, 280, 400), main = r(300, 100, 900, 1200), panel = r(1220, 100, 360, 200);
const checks = [
  ['из основной вправо — панель', nextColumn(main, [cover, panel], 'right'), 1],
  ['из панели влево — основная', nextColumn(panel, [cover, main], 'left'), 1],
  ['из основной влево — постер', nextColumn(main, [cover, panel], 'left'), 0],
  ['из панели вправо — ничего', nextColumn(panel, [cover, main], 'right'), -1],
  ['узкий экран: панель под основной — вбок ничего', nextColumn(r(0, 100, 390, 800), [r(0, 950, 390, 200)], 'right'), -1],
  ['вход: запомненный важнее главного', enterColumn([r(0, 0, 10, 10), r(0, 50, 10, 10)], r(0, 55, 5, 5), 0, 1), 1],
  ['вход: главный, если не помним', enterColumn([r(0, 0, 10, 10), r(0, 50, 10, 10)], r(0, 55, 5, 5), 0, -1), 0],
  ['вход: ближайший по высоте', enterColumn([r(0, 0, 10, 10), r(0, 50, 10, 10)], r(0, 52, 5, 5), -1, -1), 1],
  ['вход в пустую колонку', enterColumn([], r(0, 0, 5, 5), -1, -1), -1],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Escape в поле ввода (хвост Х25): фокус остаётся на поле, а следующая стрелка влево или вправо уводит
// с поля, а не двигает курсор; без Escape — курсор двигается, пока не упрётся в край.
func TestPultNavEscapeKeepsField(t *testing.T) {
	node := lookNode(t)
	script := `
import { arrowMoves } from './nav.js';
const field = { tagName: 'INPUT', type: 'text', value: 'абв', selectionStart: 1, selectionEnd: 1 };
const btn = { tagName: 'BUTTON' };
const checks = [
  ['кнопка — стрелка двигает фокус', arrowMoves('right', btn, null), true],
  ['поле, курсор в середине — стрелка двигает курсор', arrowMoves('right', field, null), false],
  ['поле после Escape — стрелка уходит с поля', arrowMoves('right', field, field), true],
  ['поле, вниз — всегда уходит', arrowMoves('down', field, null), true],
  ['поле, курсор в конце — вправо уходит', arrowMoves('right', { ...field, selectionStart: 3, selectionEnd: 3 }, null), true],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Окно подтверждения (спека 11b, 4.1): «Да» — true, Escape — false; пока окно открыто, остальное
// недоступно (inert), фокус — на «Да»; после — фокус там, где был, inert снят.
func TestPultDialog(t *testing.T) {
	node := lookNode(t)
	script := `
class El {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.listeners = {}; this.style = {}; this.dataset = {}; this.parent = null; this.inert = false; }
  append(...kids) { for (const k of kids) { const c = typeof k === 'string' ? Object.assign(new El('#text'), { text: k }) : k; c.parent = this; this.children.push(c); } }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this); this.parent = null; }
  setAttribute(k, v) { this.attrs[k] = v; if (k.startsWith('data-')) this.dataset[k.slice(5).replace(/-(.)/g, (_, c) => c.toUpperCase())] = v; }
  getAttribute(k) { return this.attrs[k] ?? null; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  focus() { if (this.isConnected) document.activeElement = this; }
  get isConnected() { let n = this; while (n.parent) n = n.parent; return n === document.body; }
  find(key) { if (this.dataset.key === key) return this; for (const c of this.children) { const f = c.find && c.find(key); if (f) return f; } return null; }
  fire(type, ev = {}) { const e = { key: ev.key, target: this, preventDefault() {}, stopPropagation() { this.stopped = true; } }; for (let n = this; n && !e.stopped; n = n.parent) for (const fn of n.listeners[type] || []) fn(e); }
}
globalThis.Node = El;
const body = new El('body');
globalThis.document = { body, activeElement: body, createElement: (t) => new El(t), createElementNS: (_, t) => new El(t),
  querySelector: (sel) => { const m = /^\[data-key="(.*)"\]$/.exec(sel); return m ? body.find(m[1]) : null; } };
globalThis.CSS = { escape: (s) => s };
const view = new El('main'); body.append(view);
const before = new El('button'); before.setAttribute('data-key', 'pick-0'); view.append(before); before.focus();
const { confirmDialog } = await import('./ui.js');
const checks = [];
let p = confirmDialog({ title: 'Скачать «Фонари» — 19,4 ГБ?' });
checks.push(['пока окно открыто, экран недоступен', view.inert === true]);
const yes = body.find('dlg-yes');
checks.push(['фокус на «Да»', document.activeElement === yes]);
yes.fire('click');
checks.push(['«Да» — true', (await p) === true]);
checks.push(['окно убрано', body.children.length === 1 && view.inert === false]);
checks.push(['фокус вернулся', document.activeElement === before]);
p = confirmDialog({ title: 'Скачать?' });
body.find('dlg-no').fire('keydown', { key: 'Escape' });
checks.push(['Escape — false', (await p) === false]);
checks.push(['после Escape экран доступен', view.inert === false && document.activeElement === before]);
// Экран перерисовался, пока окно было открыто (опрос раздачи): фокус — на новый элемент с тем же ключом.
p = confirmDialog({ title: 'Скачать?' });
before.remove();
const redrawn = new El('button'); redrawn.setAttribute('data-key', 'pick-0'); view.append(redrawn);
body.find('dlg-no').fire('click');
await p;
checks.push(['после перерисовки фокус — на тот же ключ', document.activeElement === redrawn]);
// Ревью 14В: безвозвратное (удалить своё с диска) — фокус на «Нет»: второе OK на пульте ТВ не удаляет.
p = confirmDialog({ title: 'Удалить «Фильм» с диска навсегда?', yes: 'Удалить', safe: true });
checks.push(['безвозвратное — фокус на «Нет»', document.activeElement === body.find('dlg-no')]);
body.find('dlg-no').fire('click');
checks.push(['«Нет» — false', (await p) === false]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Серия нескачанного сериала (замечание № 3 этапа 11b): OK на строке — окно «Скачать?»; после «Скачать»
// строка выбирает файл панели; пока идёт действие — ничего.
func TestPultReleaseEpisodeConfirm(t *testing.T) {
	node := lookNode(t)
	script := `
import { episodeAction } from './views/release.js';
const checks = [
  ['до «Скачать» — окно', episodeAction(false, false), 'confirm'],
  ['после «Скачать» — выбрать файл панели', episodeAction(true, false), 'pick'],
  ['идёт действие — ничего', episodeAction(false, true), 'none'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Адреса настроек (замечание № 5 этапа 11b): «Не распознано» — вкладка «Каналов»; старый адрес
// переадресуется, пустой — «Состояние».
func TestPultSettingsRoutes(t *testing.T) {
	node := lookNode(t)
	script := `
import { settingsRoute } from './views/settings-layout.js';
const s = (parts) => JSON.stringify(settingsRoute(parts));
const checks = [
  [s([]), '{"view":"status"}'],
  [s(['iptv']), '{"view":"iptv"}'],
  [s(['iptv', 'unrecognized']), '{"view":"unrecognized"}'],
  [s(['unrecognized']), '{"redirect":"#/settings/iptv/unrecognized"}'],
  [s(['library']), '{"view":"library"}'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Каталог подгружается при прокрутке (замечание № 9 этапа 11b): следующая порция — одна за раз, по
// курсору (место последней карточки, ревью 11b-Г); конец списка — больше не просим; ошибка — снова.
func TestPultCatalogPortions(t *testing.T) {
	node := lookNode(t)
	script := `
import { portions } from './views/catalog.js';
const e = (id) => ({ id });
let s = portions(undefined, { type: 'init' });
const checks = [];
checks.push(['с начала', s.next === -1 && s.more === true]);
s = portions(s, { type: 'more' });
checks.push(['первая порция просится', s.loading === true && s.page === 0]);
checks.push(['вторая просьба во время загрузки — без изменений', portions(s, { type: 'more' }) === s]);
s = portions(s, { type: 'loaded', list: { entries: [e(1), e(2)], next: 7, more: true } });
checks.push(['порция 1', s.loaded.length === 2 && !s.loading && s.page === 1 && s.next === 7]);
s = portions(s, { type: 'more' });
s = portions(s, { type: 'failed', error: 'нет сети' });
checks.push(['ошибка — не загружается, текст есть', !s.loading && s.error === 'нет сети' && s.loaded.length === 2]);
s = portions(s, { type: 'more' });
checks.push(['после ошибки можно снова', s.loading === true && s.error === '']);
s = portions(s, { type: 'loaded', list: { entries: [e(3)], next: 9, more: false } });
checks.push(['порция 2', s.loaded.map((x) => x.id).join() === '1,2,3' && s.page === 2 && s.next === 9]);
checks.push(['конец списка — больше не просим', portions(s, { type: 'more' }) === s]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Исключение внутри опроса пульта (хвост Х19) — в консоль, опрос идёт дальше.
func TestPultPollSurvivesError(t *testing.T) {
	node := lookNode(t)
	script := `
globalThis.document = { hidden: false, addEventListener() {}, removeEventListener() {} };
console.error = () => {};
const { poll } = await import('./ui.js');
let calls = 0;
const p = poll(async () => {
  calls++;
  if (calls === 1) throw new Error('сервер ответил не то');
}, 10);
await new Promise((r) => setTimeout(r, 80));
p.stop();
if (calls < 3) {
  process.stderr.write('опрос остановился после исключения: вызовов ' + calls + '\n');
  process.exitCode = 1;
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Поиск (замечание № 10 этапа 11b): опрос идёт, пока трекеры ищут (до 30 с), и дальше — пока у найденного
// догружаются страницы с постерами, но не дольше 2 минут от начала.
func TestPultSearchPolling(t *testing.T) {
	node := lookNode(t)
	script := `
import { keepPolling } from './views/search.js';
const pending = { complete: true, results: [{ detailsPending: true }, { detailsPending: false }] };
const done = { complete: true, results: [{ detailsPending: false }] };
const checks = [
  ['ищут — опрос идёт', keepPolling({ complete: false, results: [] }, 0, 10000), true],
  ['ищут дольше 30 с — хватит', keepPolling({ complete: false, results: [] }, 0, 31000), false],
  ['нашли, постеры догружаются — опрос идёт', keepPolling(pending, 0, 60000), true],
  ['догружаются дольше 2 минут — хватит', keepPolling(pending, 0, 121000), false],
  ['всё догружено — хватит', keepPolling(done, 0, 5000), false],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Загрузки» (хвост Х27): надпись «Сейчас смотрят — …» после удаления раздачи исчезает через 10 с;
// ошибка действия — остаётся до следующего действия. «Состояние» (хвост Х23): текст входа Rutracker, который
// повторяет строку трекера, второй раз не показывается.
func TestPultDownloadNoteAndLoginText(t *testing.T) {
	node := lookNode(t)
	script := `
import { noteText } from './views/downloads.js';
import { loginExtra } from './views/settings-status.js';
const checks = [
  ['надпись свежая', noteText({ text: 'Сейчас смотрят — 1 серия осталась', until: 10000 }, 5000), 'Сейчас смотрят — 1 серия осталась'],
  ['надпись через 10 с', noteText({ text: 'Сейчас смотрят — 1 серия осталась', until: 10000 }, 10001), ''],
  ['ошибка без срока', noteText({ text: 'этот файл не скачан', until: 0 }, 99999), 'этот файл не скачан'],
  ['нет записи', noteText(undefined, 1), ''],
  ['вход повторяет строку трекера', loginExtra('Rutracker: неверный логин или пароль', 'Неверный логин или пароль'), ''],
  ['вход — новое', loginExtra('Rutracker: не отвечает', 'Неверный логин или пароль'), 'Неверный логин или пароль'],
  ['строки трекера нет', loginExtra('', 'Вход выполнен'), 'Вход выполнен'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', JSON.stringify(got), 'ждали', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Настройки → Медиатека» после «Разрешить доступ» (хвост Х41): категории перечитываются, пока у какой-то
// папки нет доступа.
func TestPultLibraryGrantWatch(t *testing.T) {
	node := lookNode(t)
	script := `
import { stillDenied } from './views/settings-library.js';
const cats = (problem) => [{ folders: [{ problem: '' }] }, { folders: [{ problem }] }];
const checks = [
  ['есть папка без доступа', stillDenied(cats('no_access')), true],
  ['доступ выдан', stillDenied(cats('')), false],
  ['нет категорий', stillDenied(null), false],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Подгрузка каталога (найдено вживую, 11b-А): вторая просьба, пока порция грузится, ждёт её, а не
// возвращается сразу — иначе экран считал список пустым и не восстанавливал место после «Назад».
func TestPultOneAtATime(t *testing.T) {
	node := lookNode(t)
	script := `
import { oneAtATime } from './views/catalog.js';
let calls = 0;
let release;
const f = oneAtATime(() => { calls++; return new Promise((r) => { release = r; }); });
const a = f();
const b = f();
const checks = [['один вызов, пока идёт', calls === 1], ['тот же промис', a === b]];
release();
await a;
const c = f();
release();
await c;
checks.push(['после окончания — снова вызов', calls === 2]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Пульт ТВ: кнопка, которую нажали, на время действия отключается («Скачать», «Смотреть», «Искать на
// трекерах») — фокусу некуда встать, но когда она снова доступна, фокус возвращается на неё, а не
// теряется до первого элемента экрана. Если человек сам ушёл фокусом в другое место — его не перебивать
// (финальное ревью 7b).
func TestPultKeepFocus(t *testing.T) {
	node := lookNode(t)
	script := `
const body = { tag: 'body' };
globalThis.document = { activeElement: body, body };
globalThis.CSS = { escape: (s) => s };
const { keepFocus } = await import('./ui.js');
const el = (key, disabled = false) => ({ dataset: { key }, disabled, focus() { if (!this.disabled) document.activeElement = this; } });
let kids = [];
const root = { contains: (e) => kids.includes(e), querySelector: (sel) => kids.find((k) => sel === '[data-key="' + k.dataset.key + '"]') || null };
// redraw — перерисовка части экрана: старые элементы уходят, фокус с них — на body.
const redraw = (...next) => () => { if (kids.includes(document.activeElement) && !next.includes(document.activeElement)) document.activeElement = body; kids = next; };
const checks = [];
kids = [el('watch-1')]; kids[0].focus();
keepFocus(root, redraw(el('watch-1', true)));
checks.push(['пока кнопка отключена, фокуса на ней нет', document.activeElement === body]);
const back = el('watch-1');
keepFocus(root, redraw(back));
checks.push(['кнопка снова доступна — фокус на ней', document.activeElement === back]);
kids = [el('watch-2')]; kids[0].focus();
keepFocus(root, redraw(el('watch-2', true)));
const other = { dataset: { key: 'elsewhere' }, focus() {} };
document.activeElement = other;
keepFocus(root, redraw(el('watch-2')));
checks.push(['фокус в другом месте не перебит', document.activeElement === other]);
document.activeElement = body;
keepFocus(root, redraw(el('watch-2')));
checks.push(['ушёл сам — старый ключ забыт', document.activeElement === body]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Медиатека (спека этапа 9, раздел 6): вкладки, подписи «Продолжить», сезоны и серии, версия по
// умолчанию, ссылка плеера на Android (серии подряд — .m3u8, фильм — поток с местом).
func TestPultLibrary(t *testing.T) {
	node := lookNode(t)
	script := `
import { libraryTabs, continueLabel } from './views/library.js';
import { seasonsOf, episodeLabel, defaultVersion, libraryPlayerLink } from './views/library-card.js';
const tabs = (v) => libraryTabs(v).map((x) => x.id + ':' + x.name + ':' + x.count).join(' ');
const ep = (file, season, section, episode, name) => ({ file, season, section, episode, name });
const eps = [ep(5, 2, '', 1, 'S02E01.mkv'), ep(1, 1, '', 1, 'S01E01.mkv'), ep(2, 1, '', 2, 'S01E02.mkv'), ep(9, 0, '1. О курсе', 0, 'a.mp4'), ep(8, 0, '', 0, 'intro.mp4')];
const groups = (list) => seasonsOf(list).map((g) => g.title + '=' + g.items.map((e) => e.file).join(',')).join(' | ');
const android = 'Mozilla/5.0 (Linux; Android 12) Chrome/120';
const res = { streamUrl: 'http://192.168.0.2:8090/media/7/film.mkv', m3uUrl: 'http://192.168.0.2:8090/m3u/library/7.m3u8?start=600', title: 'Фильм', startSec: 600 };
const checks = [
  [tabs({ categories: [{ id: 1, name: 'Фильмы', count: 3 }, { id: 2, name: 'Сериалы', count: 2 }], unrecognized: 0 }), 'all:Все:5 1:Фильмы:3 2:Сериалы:2'],
  [tabs({ categories: [{ id: 1, name: 'Фильмы', count: 3 }], unrecognized: 2 }), 'all:Все:3 1:Фильмы:3 unrecognized:Не распознано:2'],
  [continueLabel({ season: 1, episode: 5, positionSec: 1380 }), '1×05, с 23 мин'],
  [continueLabel({ season: 0, episode: 0, positionSec: 3730 }), 'с 1:02:10'],
  [continueLabel({ season: 1, episode: 5, positionSec: 0 }), '1×05'],
  [continueLabel({ season: 0, episode: 0, positionSec: 0 }), 'Смотреть'],
  // Без сезона — первыми (вступление курса), потом сезоны по номеру, потом главы — как на сервере.
  [groups(eps), '=8 | Сезон 1=1,2 | Сезон 2=5 | 1. О курсе=9'],
  [groups([ep(3, 0, '', 0, 'film.mkv')]), '=3'],
  [episodeLabel(ep(2, 1, '', 2, 'S01E02.mkv')), '1×02'],
  [episodeLabel(ep(2, 0, '', 7, 'Серия 7.mp4')), '7'],
  [episodeLabel(ep(2, 0, 'Глава', 0, 'Урок про слайсы.mp4')), 'Урок про слайсы'],
  [defaultVersion({ lastVersion: 12, versions: [{ unit: 10, source: 'Фильмы' }, { unit: 11, source: 'Скачано' }, { unit: 12, source: 'Фильмы' }] }), 12],
  [defaultVersion({ lastVersion: 0, versions: [{ unit: 10, source: 'Фильмы' }, { unit: 11, source: 'Скачано' }] }), 11],
  [defaultVersion({ lastVersion: 99, versions: [{ unit: 10, source: 'Фильмы' }] }), 10],
  [libraryPlayerLink(res, true, 'Mozilla/5.0 (Windows NT 10.0)'), res.m3uUrl],
  [libraryPlayerLink(res, true, android).startsWith('intent://192.168.0.2:8090/m3u/library/7.m3u8?start=600#Intent;scheme=http;type=audio/x-mpegurl;package=org.videolan.vlc;'), true],
  [libraryPlayerLink(res, false, android).startsWith('intent://192.168.0.2:8090/media/7/film.mkv#Intent;scheme=http;type=video/*;package=org.videolan.vlc;l.position=600000;'), true],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Обзор папок и «Разрешить доступ» (спека этапа 11a, разделы 4.7 и 7): части пути для «Вверх» и
// строки пути; кнопка доступа — только на ПК с Kinodom, с телефона — строка (Review Focus 5).
func TestPultFolders(t *testing.T) {
	node := lookNode(t)
	script := `
import { crumbs, grantView } from './views/folders.js';
const c = (p) => crumbs(p).map((x) => x.name + '=' + x.path).join(' | ');
const noAccess = { path: 'D:\\Share\\Мои фильмы', problem: 'no_access' };
const checks = [
  [c('D:\\Share\\Movies'), 'D:=D:\\ | Share=D:\\Share | Movies=D:\\Share\\Movies'],
  [c('C:\\'), 'C:=C:\\'],
  [c('e:\\A\\'), 'E:=E:\\ | A=E:\\A'],
  [c(''), ''],
  [JSON.stringify(grantView({ local: true }, noAccess, 8090)), JSON.stringify({ link: 'kinodom://grant?path=D%3A%5CShare%5C%D0%9C%D0%BE%D0%B8+%D1%84%D0%B8%D0%BB%D1%8C%D0%BC%D1%8B&port=8090' })],
  [JSON.stringify(grantView({ local: false }, noAccess, 8090)), JSON.stringify({ hint: 'Откройте пульт на ПК с Kinodom, чтобы разрешить доступ' })],
  [grantView({ local: true }, { path: 'D:\\A', problem: '' }, 8090), null],
  [grantView({ local: true }, { path: 'D:\\A', problem: 'not_found' }, 8090), null],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Мастер начальных настроек (спека этапа 11a, раздел 7): пять шагов; открывается сам только из
// домашней сети, пока не пройден, и только вместо каталога — ссылка на раздачу ведёт на раздачу;
// у шага Кинопоиска — инструкция из трёх пунктов со ссылкой на сайт ключей.
func TestPultSetup(t *testing.T) {
	node := lookNode(t)
	script := `
import * as setup from './views/setup.js';
const { STEPS, nextStep, prevStep, shouldOpenSetup } = setup;
const home = { canEdit: true, setupDone: false };
const checks = [
  // Шага «Кинопоиск» нет (спека 11b, 5.7): Кинопоиск работает без ключа.
  [STEPS.map((s) => s.id).join(','), 'trackers,channels,library,done'],
  [setup.kpInstruction, undefined],
  [nextStep(0), 1], [nextStep(STEPS.length - 1), STEPS.length - 1], [prevStep(0), 0], [prevStep(3), 2],
  [shouldOpenSetup(home, ''), true],
  [shouldOpenSetup(home, '#/'), true],
  [shouldOpenSetup(home, '#/catalog/rutor'), true],
  [shouldOpenSetup(home, '#/release/12'), false],
  [shouldOpenSetup(home, '#/settings/params'), false],
  [shouldOpenSetup({ canEdit: false, setupDone: false }, ''), false],
  [shouldOpenSetup({ canEdit: true, setupDone: true }, ''), false],
  [shouldOpenSetup(null, ''), false],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Разрешить доступ» сообщает экрану о нажатии: мастер снова опрашивает медиатеку, иначе после окна
// Windows «Да/Нет» папка так и показывалась бы «нет доступа» (второе ревью, мелочь 1).
func TestPultGrantControlNotifies(t *testing.T) {
	node := lookNode(t)
	script := `
const mk = (tag) => ({ tag, attrs: {}, listeners: {}, children: [], style: {},
  setAttribute(k, v) { this.attrs[k] = v; }, addEventListener(k, f) { this.listeners[k] = f; }, append(...k) { this.children.push(...k); } });
globalThis.document = { createElement: mk, createElementNS: (ns, tag) => mk(tag) };
globalThis.Node = class {};
const { grantControl } = await import('./views/folders.js');
let pressed = 0;
const link = grantControl({ local: true }, { path: 'D:\\A', problem: 'no_access' }, 'grant-1', () => pressed++);
link.listeners.click();
const hint = grantControl({ local: false }, { path: 'D:\\A', problem: 'no_access' }, 'grant-1', () => pressed++);
const checks = [
  [link.attrs.href.startsWith('kinodom://grant?'), true],
  [pressed, 1],
  [hint.listeners.click, undefined],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Скрытие» в «Настройках → Каналы»: отметка сразу включает «Сохранить» (найдено вживую, 11b-А: кнопка
// включалась только перерисовкой опроса раз в 5 с — с пульта ТВ фокус на неё не попадал).
func TestPultHideCardSaveEnables(t *testing.T) {
	node := lookNode(t)
	script := `
globalThis.Node = class {};
class El extends Node {
  constructor(tag) { super(); this.tag = tag; this.attrs = {}; this.listeners = {}; this.children = []; this.style = {}; }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener(k, f) { this.listeners[k] = f; }
  append(...k) { this.children.push(...k); }
  all() { return [this, ...this.children.flatMap((c) => (c instanceof El ? c.all() : []))]; }
}
globalThis.document = { createElement: (t) => new El(t), createElementNS: (_, t) => new El(t) };
const { hideCard } = await import('./views/settings-iptv.js');
const iv = { hiddenCategories: [], hiddenCountries: [], hiddenLanguages: [] };
const all = { categories: [], countries: [{ id: 'RU', name: 'Россия', count: 2 }],
  languages: [{ id: 'rus', name: 'русский', count: 2 }, { id: 'eng', name: 'английский', count: 1 }] };
const draft = { categories: [], countries: [], languages: [] };
let saved = 0;
const card = hideCard(iv, all, draft, true, () => saved++);
const find = (key) => card.all().find((e) => e.attrs['data-key'] === key);
const btn = find('hide-save');
const eng = find('hide-languages-eng');
const checks = [];
checks.push(['без изменений — выключена', btn.disabled === true]);
checks.push(['«Другие часовые пояса» нет — версии внутри канала (11b-Е)', !find('hide-zones')]);
eng.checked = true;
eng.listeners.change({ target: eng });
checks.push(['отметили язык — включена сразу', btn.disabled === false]);
checks.push(['черновик', JSON.stringify(draft.languages) === '["eng"]']);
eng.checked = false;
eng.listeners.change({ target: eng });
checks.push(['сняли отметку — снова выключена', btn.disabled === true]);
btn.listeners.click();
checks.push(['нажатие сохраняет', saved === 1]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Возврат в каталог, который стал короче (склейка карточек, новый топ), — без бесконечного цикла:
// глубина — не больше, чем есть у сервера, и порция, которая не пришла, не просится снова (ревью 11b-А).
func TestPultRestoreDepth(t *testing.T) {
	node := lookNode(t)
	script := `
import { restoreDepth } from './views/catalog.js';
const checks = [];
const run = async (name, start, serverPages, saved, pageOnMore) => {
  let state = { ...start };
  let calls = 0;
  const more = async () => {
    calls++;
    if (calls > 50) throw new Error('цикл');
    state = pageOnMore(state);
  };
  const t = setTimeout(() => { console.error(name, ': завис'); process.exit(1); }, 2000);
  await restoreDepth(more, () => state, saved);
  clearTimeout(t);
  return { state, calls };
};
const grow = (pages) => (s) => (s.page < pages ? { ...s, page: s.page + 1, more: s.page + 1 < pages } : s);
let r = await run('каталог стал короче', { page: 4, more: false, error: '' }, 4, 5, grow(4));
checks.push(['короче — сразу выход', r.calls === 0]);
r = await run('догрузка до сохранённой', { page: 1, more: true, error: '' }, 9, 3, grow(9));
checks.push(['до сохранённой глубины', r.state.page === 3 && r.calls === 2]);
r = await run('сервер отдаёт меньше', { page: 1, more: true, error: '' }, 2, 5, grow(2));
checks.push(['не глубже сервера', r.state.page === 2 && r.calls === 1]);
r = await run('порция не пришла', { page: 1, more: true, error: '' }, 5, 4, (s) => s);
checks.push(['без изменений — выход после одной просьбы', r.calls === 1]);
r = await run('ошибка', { page: 1, more: true, error: 'нет сети' }, 5, 4, grow(5));
checks.push(['ошибка — выход', r.calls === 0]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Повтор порции после ошибки — прокруткой или «вниз» из последнего ряда, когда низ сетки рядом (Review
// Focus 1; ревью 11b-А: наблюдатель пересечения второй раз не срабатывает, пока низ не ушёл из зоны).
func TestPultRetryDue(t *testing.T) {
	node := lookNode(t)
	script := `
import { retryDue } from './views/catalog.js';
const failed = { error: 'нет сети', loading: false, page: 2, more: true };
const checks = [
  ['ошибка, низ рядом — повтор', retryDue(failed, 900, 800), true],
  ['ошибка, низ далеко — нет', retryDue(failed, 2000, 800), false],
  ['без ошибки — наблюдатель сам', retryDue({ ...failed, error: '' }, 900, 800), false],
  ['идёт загрузка — нет', retryDue({ ...failed, loading: true }, 900, 800), false],
  ['список кончился — нет', retryDue({ ...failed, more: false }, 900, 800), false],
  ['первая порция не пришла — повтор', retryDue({ error: 'нет сети', loading: false, page: 0, more: true }, 100, 800), true],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Обратно в колонку — на элемент, с которого ушли, даже если экран его пересоздал (строки серий
// перерисовываются раз в 1–3 с): по data-key (ревью 11b-А).
func TestPultRememberedByKey(t *testing.T) {
	node := lookNode(t)
	script := `
import { rememberedIndex } from './nav.js';
const el = (key) => ({ dataset: { key } });
const a = el('ep-1'), b = el('ep-2'), c = el('ep-3');
const redrawn = [el('ep-1'), el('ep-2'), el('ep-3')];
const checks = [
  ['тот же элемент', rememberedIndex([a, b, c], { el: b, key: 'ep-2' }), 1],
  ['перерисовали — по ключу', rememberedIndex(redrawn, { el: b, key: 'ep-2' }), 1],
  ['ключа больше нет', rememberedIndex([a, c], { el: b, key: 'ep-2' }), -1],
  ['не уходили', rememberedIndex([a, b], undefined), -1],
  ['без ключа, элемент пересоздан', rememberedIndex(redrawn, { el: b, key: '' }), -1],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Ключ Кинопоиска можно стереть (Х22): «Стереть» — пустой ключ; пустое поле без «Стереть» — не менять.
func TestPultKeyErase(t *testing.T) {
	node := lookNode(t)
	script := `
import { kpKeyPatch } from './views/settings-params.js';
const s = (v) => JSON.stringify(v);
const checks = [
  [s(kpKeyPatch('', true)), '{"key":""}'],
  [s(kpKeyPatch('новый', true)), '{"key":""}'],
  [s(kpKeyPatch(' abc ', false)), '{"key":"abc"}'],
  [s(kpKeyPatch('  ', false)), 'null'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Состояние» → Кинопоиск (спека 11b, 5.7): без токена — работает или пауза до ЧЧ:ММ; ключ — не задан
// (не жёлтым), N из M, не подошёл, квота до ЧЧ:ММ.
func TestPultStatusKinopoisk(t *testing.T) {
	node := lookNode(t)
	script := `
import { kpLine } from './views/settings-status.js';
const base = { keySet: false, badKey: false, dailyUsed: 0, dailyLimit: 0, quotaUntil: null, keyless: { pausedUntil: null, reason: '', today: 3 } };
const checks = [];
let r = kpLine(base);
checks.push(['без ключа', r.text === 'без токена: работает · ключ не задан' && !r.warn]);
r = kpLine({ ...base, keyless: { pausedUntil: '2026-09-30T18:05:00Z', reason: 'капча', today: 40 } });
checks.push(['пауза', /^без токена: пауза до \d\d:\d\d \(капча\) · ключ не задан$/.test(r.text) && r.warn]);
r = kpLine({ ...base, keySet: true, dailyUsed: 20, dailyLimit: 500 });
checks.push(['ключ задан', r.text === 'без токена: работает · ключ: 20 из 500 за сутки' && !r.warn]);
r = kpLine({ ...base, keySet: true, badKey: true });
checks.push(['ключ не подошёл', r.text.endsWith('ключ не подошёл') && r.warn]);
r = kpLine({ ...base, keySet: true, quotaUntil: '2026-09-30T19:00:00Z' });
checks.push(['квота', /ключ: квота до \d\d:\d\d$/.test(r.text)]);
for (const [name, ok] of checks) {
  if (!ok) {
    console.error('не выполнено:', name, JSON.stringify(r));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Атрибут hidden скрывает и элементы с display в стилях (.col — flex): иначе «Дополнительно» в
// «Параметрах» не сворачивалось (найдено вживую, 11b-Б).
func TestPultHiddenWins(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\[hidden\]\s*\{\s*display:\s*none\s*!important`).Match(css) {
		t.Fatal("в style.css нет [hidden] { display: none !important }")
	}
}

// Каталог Rutracker: ряд групп и ряд подразделов выбранной группы; у Rutor групп нет (спека 11b, 7.1).
func TestPultGroupBar(t *testing.T) {
	node := lookNode(t)
	script := `
import { groupBar } from './views/catalog.js';
const secs = [
  { id: '7', name: 'Зарубежное кино', group: 'c2', groupName: 'Кино' },
  { id: '22', name: 'Наше кино', group: 'c2', groupName: 'Кино' },
  { id: '189', name: 'Зарубежные сериалы', group: 'c18', groupName: 'Сериалы' },
  { id: '46', name: 'Документальные', group: 'c20', groupName: 'Документалистика' },
];
const s = (v) => JSON.stringify(v);
let r = groupBar(secs, '189');
const checks = [
  ['группы', s(r.groups.map((g) => g.name)), '["Кино","Сериалы","Документалистика"]'],
  ['выбранная — по разделу', s(r.groups.filter((g) => g.on).map((g) => g.id)), '["c18"]'],
  ['подразделы группы', s(r.sections.map((x) => x.id)), '["189"]'],
  ['ссылка группы — на первый её подраздел', s(r.groups.map((g) => g.first)), '["7","189","46"]'],
];
r = groupBar(secs, '');
checks.push(['без раздела — первая группа', s(r.sections.map((x) => x.id)), '["7","22"]']);
r = groupBar([{ id: '12', name: 'Зарубежные фильмы', group: '' }, { id: '4', name: 'Сериалы', group: '' }], '4');
checks.push(['Rutor — без групп', s([r.groups.length, r.sections.length]), '[0,2]']);
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Разделы каталога» Rutracker: три группы и подразделы первого уровня (спека 11b, 7.1). Прежний выбор —
// подфорумы, «раздел+», «cN+» — отмечает свои подразделы первого уровня; сохраняется «подраздел+».
func TestPultSectionsGroups(t *testing.T) {
	node := lookNode(t)
	script := `
import { buildTree, decodeGroups, encodeGroups } from './views/settings-sections.js';
const full = buildTree([
  { id: 'c2', name: 'Кино, Видео и ТВ', parentId: '' }, { id: 'c20', name: 'Документалистика и юмор', parentId: '' },
  { id: '7', name: 'Зарубежное кино', parentId: 'c2' }, { id: '252', name: 'Фильмы 2026', parentId: '7' },
  { id: '22', name: 'Наше кино', parentId: 'c2' },
  { id: '1629', name: 'Предложения', parentId: 'c20' }, { id: '19', name: 'СМИ', parentId: 'c20' },
  { id: '46', name: 'Документальные', parentId: 'c20' }, { id: '2076', name: '[Док] Космос', parentId: '46' },
  { id: '314', name: 'Документальные (HD Video)', parentId: 'c20' }, { id: '2110', name: '[HD] Природа', parentId: '314' },
]);
const groups = buildTree([
  { id: 'c2', name: 'Кино', parentId: '' }, { id: '7', name: 'Зарубежное кино', parentId: 'c2' }, { id: '22', name: 'Наше кино', parentId: 'c2' },
  { id: 'c20', name: 'Документалистика', parentId: '' }, { id: '19', name: 'СМИ', parentId: 'c20' },
  { id: '46', name: 'Документальные', parentId: 'c20' }, { id: '314', name: 'Документальные (HD Video)', parentId: 'c20' },
]);
const dec = (entries) => [...decodeGroups(full, groups, entries)].sort().join(',');
const checks = [
  ['подфорумы — свои подразделы', dec(['2110', '2076']), '314,46'],
  ['группа целиком — без служебных', dec(['c20+']), '19,314,46'],
  ['подраздел+ и подраздел', dec(['46+', '7']), '46,7'],
  ['чего нет в дереве — пропуск', dec(['999', '252']), '7'],
  ['сохранение — подраздел+, по порядку групп', encodeGroups(groups, new Set(['46', '22'])).join(','), '22+,46+'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Вживую 11b-Г: браузер держит прокрутку за низом сетки (якорь прокрутки) — порция пришла, а низ остался
// в зоне наблюдателя, нового события нет, подгрузка вставала и перескакивала пришедшую порцию. Низ сетки
// не якорь; пришла порция, а низ всё ещё рядом — следующая сразу.
func TestPultFillDue(t *testing.T) {
	node := lookNode(t)
	script := `
import { fillDue } from './views/catalog.js';
const st = { error: '', loading: false, page: 6, more: true };
const checks = [
  ['низ рядом — следующая', fillDue(st, 852, 900), true],
  ['низ далеко — наблюдатель сам', fillDue(st, 2700, 900), false],
  ['ошибка — не сама (повтор — прокруткой)', fillDue({ ...st, error: 'нет сети' }, 852, 900), false],
  ['идёт загрузка — нет', fillDue({ ...st, loading: true }, 852, 900), false],
  ['список кончился — нет', fillDue({ ...st, more: false }, 852, 900), false],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ': получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\.grid-tail\s*\{[^}]*overflow-anchor:\s*none`).Match(css) {
		t.Error("низ сетки — якорь прокрутки: .grid-tail без overflow-anchor: none")
	}
}

// «Следить» — у сериала и только из домашней сети; следят — «Не следить» (спека 11b, 6.1).
func TestPultFollowButton(t *testing.T) {
	node := lookNode(t)
	script := `
import { followButton } from './views/release.js';
const s = (v) => JSON.stringify(v);
const checks = [
  ['сериал — «Следить»', s(followButton({ series: true, follow: '' }, true)), s({ label: 'Следить', on: false })],
  ['следят — «Не следить»', s(followButton({ series: true, follow: 'active' }, true)), s({ label: 'Не следить', on: true })],
  ['закончилась — снова «Следить»', s(followButton({ series: true, follow: 'finished' }, true)), s({ label: 'Следить', on: false })],
  ['фильм — нет', s(followButton({ series: false, follow: '' }, true)), 'null'],
  ['не из дома — нет', s(followButton({ series: true, follow: '' }, false)), 'null'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Строка «Новых серий»: «<название> — 1×07»; снятая — «— Раздача снята с трекера»; число у колокольчика.
func TestPultUpdateLabel(t *testing.T) {
	node := lookNode(t)
	script := `
import { updateLabel, bellText } from './views/updates.js';
const checks = [
  ['серии', updateLabel({ name: 'Холод', title: 'Холод [01-07 из 08] (2026)', kind: 'episodes', label: '1×07–1×08' }), 'Холод — 1×07–1×08'],
  ['без имени — название раздачи', updateLabel({ name: '', title: 'Холод [01-07 из 08]', kind: 'episodes', label: '1×07' }), 'Холод [01-07 из 08] — 1×07'],
  ['снята', updateLabel({ name: 'Холод', kind: 'removed', label: 'Раздача снята с трекера' }), 'Холод — Раздача снята с трекера'],
  ['колокольчик пуст', bellText(0), ''],
  ['колокольчик', bellText(3), '3'],
  ['колокольчик много', bellText(120), '99+'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Подписи трекеров (11b-Д): свои, источник поиска и трекеры раздач из него; незнакомый — с заглавной.
// Теги поиска: источник — после своих трекеров, число — раздачи чужих трекеров. Чужая раздача — «назад»
// в поиск, своя — в раздел каталога.
func TestPultTrackerLabel(t *testing.T) {
	node := lookNode(t)
	script := `
import { trackerLabel, tagStates, backTo } from './views/search.js';
const checks = [
  [trackerLabel('rutor'), 'Rutor'], [trackerLabel('rutracker'), 'Rutracker'], [trackerLabel('jacred'), 'Jacred'],
  [trackerLabel('kinozal'), 'Kinozal'], [trackerLabel('nnmclub'), 'NNM-Club'], [trackerLabel('lostfilm'), 'LostFilm'],
  [trackerLabel('megapeer'), 'Megapeer'], [trackerLabel('bitru'), 'Bitru'], [trackerLabel('ultradox'), 'Ultradox'],
  [trackerLabel('newtracker'), 'Newtracker'], [trackerLabel(''), ''],
];
const tags = tagStates({ jacred: 'нужен ключ', rutor: 'ok', rutracker: 'идёт' }, [{ tracker: 'rutor' }, { tracker: 'kinozal' }, { tracker: 'nnmclub' }]);
checks.push([JSON.stringify(tags), JSON.stringify([
  { state: 'ok', text: 'Rutor · 1' }, { state: 'busy', text: 'Rutracker · ищет…' }, { state: 'warn', text: 'Jacred · нужен ключ' }])]);
const done = tagStates({ jacred: 'ok' }, [{ tracker: 'rutor' }, { tracker: 'kinozal' }, { tracker: 'nnmclub' }]);
checks.push([JSON.stringify(done), JSON.stringify([{ state: 'ok', text: 'Jacred · 2' }])]);
const b1 = backTo({ tracker: 'kinozal' }, '#/catalog/rutor/12', '#/search?q=x');
const b2 = backTo({ tracker: 'rutor', category: 'Фильмы' }, '#/catalog/rutor/12', '#/search?q=x');
const b3 = backTo({ tracker: 'kinozal' }, null, null);
checks.push([JSON.stringify(b1), '{"href":"#/search?q=x","text":"Поиск"}']);
checks.push([JSON.stringify(b2), '{"href":"#/catalog/rutor/12","text":"Rutor · Фильмы"}']);
checks.push([JSON.stringify(b3), '{"href":"#/search","text":"Поиск"}']);
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Ключ источника поиска — как ключ Кинопоиска: «Стереть» — пустой ключ; пустое поле — не менять.
func TestPultSearchKeyPatch(t *testing.T) {
	node := lookNode(t)
	script := `
import { searchKeyPatch } from './views/settings-params.js';
const s = (v) => JSON.stringify(v);
const checks = [
  [s(searchKeyPatch('', true)), '{"key":""}'],
  [s(searchKeyPatch(' k-1 ', false)), '{"key":"k-1"}'],
  [s(searchKeyPatch('  ', false)), 'null'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Экран раздачи, которая не сериал и без описания (раздача чужого трекера без номера Кинопоиска): ни в
// сведениях, ни в панели нет текста «null» — replaceChildren печатал бы null текстом (вживую 11b-Д).
func TestPultReleaseNoNullText(t *testing.T) {
	node := lookNode(t)
	script := `
class El {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.listeners = {}; this.style = {}; this.dataset = {}; this.parent = null;
    this.classList = { add() {}, remove() {}, toggle() {}, contains() { return false; } }; }
  append(...kids) { for (const k of kids) { const c = k instanceof El ? k : Object.assign(new El('#text'), { text: String(k) }); c.parent = this; this.children.push(c); } }
  replaceChildren(...kids) { this.children = []; this.append(...kids); }
  remove() {}
  setAttribute(k, v) { this.attrs[k] = v; if (k.startsWith('data-')) this.dataset[k.slice(5)] = v; }
  getAttribute(k) { return this.attrs[k] ?? null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener() {}
  removeEventListener() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
  contains() { return false; }
  focus() {}
  get textContent() { return this.tagName === '#TEXT' ? this.text : this.children.map((c) => c.textContent).join(''); }
  set textContent(v) { this.children = []; this.append(String(v)); }
}
globalThis.Node = El;
const body = new El('body');
globalThis.document = { body, activeElement: body, hidden: false, createElement: (t) => new El(t), createElementNS: (_, t) => new El(t),
  createTextNode: (t) => Object.assign(new El('#text'), { text: t }), addEventListener() {}, removeEventListener() {}, querySelector: () => null };
globalThis.requestAnimationFrame = (f) => setTimeout(f, 0);
globalThis.CSS = { escape: (s) => s };
const rel = { id: 7, tracker: 'bitru', title: 'Трудно быть богом 1 сезон (1-7 из 10) (2026) WEBRip', name: 'Трудно быть богом', seeders: 69, size: 1,
  hash: 'eb3968c6ca73e0c2a5e471b9d61bf0cf7393c754', trackerUrl: 'https://bitru.example/details.php?id=1', description: '', detailsPending: false, series: false, follow: '' };
globalThis.fetch = async (url) => {
  const body = url.endsWith('/releases/7') ? rel : url.includes('/variants') ? { items: [], search: null } : url.includes('/history/') ? { files: [] } : null;
  return { ok: body !== null, status: body === null ? 404 : 200, text: async () => JSON.stringify(body ?? { error: 'нет' }) };
};
const { render } = await import('./views/release.js');
const root = new El('main');
body.append(root);
const stop = render(root, { parts: ['release', '7'], query: new URLSearchParams() }, { canEdit: true, listeners: new Set(), status: null, go() {}, refreshStatus() {} });
await new Promise((r) => setTimeout(r, 300));
const text = root.textContent;
if (!text.includes('Скачать') || text.includes('null')) {
  console.error('текст экрана:', text);
  process.exitCode = 1;
}
if (typeof stop === 'function') stop();
process.exit();
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Версии канала по времени (спека 11b, 13.3): страница — ключ канала, «Смотреть», .m3u8, программа и
// настройки — ключ выбранной версии; у московской версии ключ совпадает с ключом канала (Review Focus 5).
func TestPultVersionLinks(t *testing.T) {
	node := lookNode(t)
	script := `
import { versionLinks } from './views/channel.js';
const pl4 = versionLinks({ key: 'pervy', version: 'pervy-pl4' });
const msk = versionLinks({ key: 'pervy', version: 'pervy' });
const shifted = versionLinks({ key: 'sts', version: 'sts~1' });
const checks = [
  [pl4.watch, 'pervy-pl4'], [pl4.m3u, '/m3u/channel/pervy-pl4.m3u8'], [pl4.epg, '/channels/pervy-pl4/epg'],
  [pl4.settings, '#/channel/pervy/settings?v=pervy-pl4'], [pl4.page('pervy-mn1'), '#/channel/pervy?v=pervy-mn1'],
  [pl4.api('pervy'), '/channels/pervy?version=pervy'], [pl4.api(''), '/channels/pervy'],
  [msk.watch, 'pervy'], [msk.settings, '#/channel/pervy/settings?v=pervy'],
  [shifted.m3u, '/m3u/channel/sts~1.m3u8'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Ряд версий канала на 390 px (вживую 11b-Е: МСК…МСК+7 — пять кнопок, страница прокручивалась вбок):
// ряд прокручивается сам, страница — нет. Класс — zones: «versions» занят «Есть дубли» медиатеки (столбик).
func TestPultVersionsRowScrolls(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\.seg\.zones\s*\{[^}]*overflow-x:\s*auto`).Match(css) || !regexp.MustCompile(`\.seg\.zones\s*\{[^}]*max-width:\s*100%`).Match(css) {
		t.Error(".seg.zones без max-width: 100% и overflow-x: auto")
	}
	js, err := os.ReadFile("static/views/channel.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `class: 'seg zones'`) {
		t.Error("ряд версий без класса zones")
	}
}

// Карточки каталога обновляются на месте (спека 11b, 14.1): спрашиваются только незаконченные (без страницы —
// всегда, без постера — до POSTER_TRIES раз) и только у экрана (экран вверх, два вниз), ближние первыми, не
// больше предела; перерисовывается карточка, у которой изменилось видимое.
func TestPultLiveCards(t *testing.T) {
	node := lookNode(t)
	script := `
import { POSTER_TRIES, waiting, nearest, changed } from './views/catalog.js';
const checks = [
  ['без страницы — ждём всегда', waiting({ detailsPending: true, imageKey: 'k' }, 1000), true],
  ['без постера — ждём', waiting({ detailsPending: false, imageKey: '' }, 0), true],
  ['без постера, последняя попытка', waiting({ detailsPending: false, imageKey: '' }, POSTER_TRIES - 1), true],
  ['без постера, попытки кончились', waiting({ detailsPending: false, imageKey: '' }, POSTER_TRIES), false],
  ['готова', waiting({ detailsPending: false, imageKey: 'k' }, 0), false],
  ['попыток — около минуты при опросе раз в 3 с', POSTER_TRIES, 20],
];
const cards = [
  { id: 1, top: 100, bottom: 400 }, { id: 2, top: -500, bottom: -100 }, { id: 3, top: 1000, bottom: 1300 },
  { id: 4, top: 2000, bottom: 2300 }, { id: 5, top: -1200, bottom: -900 }, { id: 6, top: 300, bottom: 600 },
];
checks.push(['у экрана, ближние первыми', JSON.stringify(nearest(cards, 800, 10)), '[1,6,2,3]']);
checks.push(['предел', JSON.stringify(nearest(cards, 800, 3)), '[1,6,2]']);
checks.push(['пусто', JSON.stringify(nearest([], 800, 10)), '[]']);
const a = { id: 1, title: '', name: '', year: 0, quality: '', format: '', imageKey: '', kinopoisk: 0, seeders: 5, size: 1, detailsPending: true, variants: 2 };
checks.push(['пришёл постер', changed(a, { ...a, imageKey: 'k' }), true]);
checks.push(['пришло название', changed(a, { ...a, title: 'Кино (2020)', name: 'Кино', year: 2020 }), true]);
checks.push(['пришёл рейтинг', changed(a, { ...a, kinopoisk: 7.1 }), true]);
checks.push(['страница загружена', changed(a, { ...a, detailsPending: false }), true]);
checks.push(['ничего', changed(a, { ...a }), false]);
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Назад» ссылкой со страницы раздачи (спека 11b, 14.3): пришли прямо из этого раздела — шаг назад по
// истории (место — в её записи); иначе — переход, а место — запомненное во вкладке, если оно этого раздела.
func TestPultBackStep(t *testing.T) {
	node := lookNode(t)
	script := `
import { backStep, catalogMemory, catalogHome } from './views/catalog.js';
const href = '#/catalog/rutor/4';
const place = { at: '#/catalog/rutor/4', key: '#/catalog/rutor/4', pages: 3, scrollY: 1800, focusKey: 'e-5' };
const bare = { ...place, at: '#/catalog/rutor' };
const other = { ...place, at: '#/catalog/rutor/12', key: '#/catalog/rutor/12' };
const checks = [
  ['пришли из раздела', backStep(href, href, place), { back: true, place: null }],
  // Ревью 15А, п. 1: память раздела — с порядком («назад» в обход), а сравнение — без него: пришли прямо из
  // раздела «Новые» — шаг назад по истории, как раньше.
  ['с порядком — пришли из раздела', backStep(catalogMemory('rutor', '12', 'new'), '#/catalog/rutor/12', { ...place, key: '#/catalog/rutor/12', at: '#/catalog/rutor/12' }), { back: true, place: null }],
  ['с порядком — переход, место без запроса', backStep(catalogMemory('rutor', '12', 'new'), '#/release/77', { ...place, key: '#/catalog/rutor/12', at: '#/catalog/rutor/12' }), { back: false, place: { ...place, key: '#/catalog/rutor/12', at: '#/catalog/rutor/12' } }],
  // Меню и запуск — без порядка из памяти раздела: действуют память устройства и умолчание из настроек.
  ['меню', catalogHome('#/catalog/rutor/12?order=new'), '#/catalog/rutor/12'],
  ['меню без памяти', catalogHome(null), '#/catalog/rutor'],
  ['пришли из раздела по адресу без раздела', backStep(href, '#/catalog/rutor', bare), { back: true, place: null }],
  ['из другой раздачи — переход с местом', backStep(href, '#/release/77', place), { back: false, place }],
  ['адрес без раздела — место по адресу ссылки', backStep(href, '#/release/77', bare), { back: false, place: { ...bare, at: href } }],
  ['место другого раздела', backStep(href, '#/release/77', other), { back: false, place: null }],
  ['первый экран', backStep(href, '', place), { back: false, place }],
  ['места нет', backStep(href, '#/release/77', null), { back: false, place: null }],
  ['пришли из другого раздела', backStep(href, '#/catalog/rutor/12', other), { back: false, place: null }],
];
for (const [name, got, want] of checks) {
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    console.error(name, ':', JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Финальное ревью 11b-Ж, Important 2: экран раздачи спрашивает её, пока не пришла страница (не дольше 2 минут),
// а после страницы — пока не пришёл постер, не дольше минуты (сервер пробует постер открытой раздачи раз в
// минуту; спека 11b, 14.2). Раньше опрос вставал на странице, и постер, начатый при открытии, не появлялся.
func TestPultReleaseDone(t *testing.T) {
	node := lookNode(t)
	script := `
import { releaseDone } from './views/release.js';
const s = 1000000;
const checks = [
  ['страницы нет, 10 с', releaseDone({ detailsPending: true }, s, 0, s + 10000), false],
  ['страницы нет, больше 2 минут', releaseDone({ detailsPending: true }, s, 0, s + 121000), true],
  ['страница и постер', releaseDone({ detailsPending: false, imageKey: 'k' }, s, s + 5000, s + 5000), true],
  ['страница без постера, 30 с', releaseDone({ detailsPending: false, imageKey: '' }, s, s + 5000, s + 35000), false],
  ['страница без постера, больше минуты', releaseDone({ detailsPending: false, imageKey: '' }, s, s + 5000, s + 66000), true],
  ['страница пришла поздно — минута на постер от неё', releaseDone({ detailsPending: false, imageKey: '' }, s, s + 110000, s + 150000), false],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Замечание № 19 (спека 11b, 15.1): «смотрели» — поток закрыт, «смотрят» — открыт; подтверждение корзины
// с временем последнего потока; «Удалить» на странице раздачи — есть хранимые файлы без открытого потока,
// в подтверждении — сколько скачано.
func TestPultDeleteLabels(t *testing.T) {
	node := lookNode(t)
	script := `
import { stateLabel, confirmText } from './views/downloads.js';
import { removable, removeTitle } from './views/release.js';
const at = new Date(2026, 9, 1, 12, 35).toISOString();
const st = (files) => ({ files });
const checks = [
  ['поток открыт', stateLabel({ state: 'watching', streaming: true }), 'смотрят'],
  ['поток закрыт', stateLabel({ state: 'watching', streaming: false }), 'смотрели'],
  ['качается', stateLabel({ state: 'downloading' }), 'качается'],
  ['подтверждение со временем', confirmText({ watchedAt: at }), 'Смотрели в 12:35 — удалить?'],
  ['подтверждение без потока', confirmText({}), 'Удалить?'],
  ['нечего удалять', removable(st([{ stored: false }])), false],
  ['качается, поток закрыт', removable(st([{ stored: true, watching: false }])), true],
  ['фильм смотрят', removable(st([{ stored: true, watching: true }])), false],
  ['одну серию смотрят — остальное удалить можно', removable(st([{ stored: true, watching: true }, { stored: true, watching: false }])), true],
  ['нет состояния', removable(null), false],
  ['скачано 23 %', removeTitle('Одиссея', st([{ stored: true, size: 1000, done: 230 }, { stored: false, size: 500, done: 0 }])), 'Удалить «Одиссея» — скачано 23 %?'],
  ['скачано целиком', removeTitle('Одиссея', st([{ stored: true, size: 1000, done: 1000 }])), 'Удалить «Одиссея»?'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Финальное ревью 11b-З, Important 1: опрос загрузки на странице раздачи не останавливается, пока у
// хранимого файла открыт поток — иначе у скачанной раздачи «Удалить» застывала серой, когда плеер закрыли.
func TestPultTorrentDone(t *testing.T) {
	node := lookNode(t)
	script := `
import { torrentDone } from './views/release.js';
const checks = [
  ['качается', torrentDone({ files: [{ stored: true, readiness: 'wait' }] }), false],
  ['скачано, плеер закрыт', torrentDone({ files: [{ stored: true, readiness: 'done', watching: false }] }), true],
  ['скачано, поток открыт', torrentDone({ files: [{ stored: true, readiness: 'done', watching: true }] }), false],
  ['ничего не хранится', torrentDone({ files: [{ stored: false }] }), false],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Состояние» (спека этапа 13, раздел 5.3): строка «Приложение для ТВ и телефона» с адресом ПК в домашней сети;
// APK на сервере нет или адреса нет — строки нет.
// Карточка «Приложение для ТВ и телефона» (правки после 13b, заказчик 2026-10-01): в браузере — «Скачать» и
// адрес для ТВ; в приложении — установленная и серверная версии, «Обновить» (новое приложение) или «Скачать»
// (приложение 13a/13b без update), одинаковые — «Последняя версия».
func TestPultAppCard(t *testing.T) {
	node := lookNode(t)
	script := `
import { appModel } from './views/settings-status.js';
const app = { version: '0.11.0-new', versionCode: 450, url: '/app/kinodom.apk', size: 1 };
const fn = (v) => () => v;
const j = (x) => JSON.stringify(x);
const checks = [
  ['APK нет', j(appModel(null, ['http://192.168.0.26:8090'], null)), 'null'],
  ['браузер', j(appModel(app, ['http://192.168.0.26:8090'], null)),
    j({ mode: 'browser', version: '0.11.0-new', download: '/app/kinodom.apk', address: 'http://192.168.0.26:8090/app' })],
  ['браузер без адреса', j(appModel(app, [], null)), j({ mode: 'browser', version: '0.11.0-new', download: '/app/kinodom.apk', address: null })],
  ['приложение старее, есть update', j(appModel(app, [], { version: fn('0.11.0-old'), versionCode: fn(443), update: () => {} })),
    j({ mode: 'app', installed: '0.11.0-old', server: '0.11.0-new', action: 'update', download: '/app/kinodom.apk' })],
  ['приложение то же', appModel(app, [], { version: fn('0.11.0-new'), versionCode: fn(450), update: () => {} }).action, 'latest'],
  ['приложение новее сервера', appModel(app, [], { version: fn('0.11.0-dev'), versionCode: fn(460), update: () => {} }).action, 'latest'],
  ['приложение 13a/13b — без versionCode и update', appModel(app, [], { version: fn('0.11.0-161d766') }).action, 'download'],
  ['приложение 13a/13b — та же версия', appModel(app, [], { version: fn('0.11.0-new') }).action, 'latest'],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 14А, задача 2: «История» в шапке между «Загрузками» и «Настройками»; метка формата — у формата в
// приоритете с классом pref, без формата — ничего.
func TestPultNavAndFormatTag(t *testing.T) {
	node := lookNode(t)
	script := `
globalThis.Node = class {};
class El extends Node {
  constructor(tag) { super(); this.attrs = {}; this.children = []; this.style = {}; }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener() {}
  append(...k) { this.children.push(...k); }
  get className() { return this.attrs.class; }
  get textContent() { return this.children.map((c) => (typeof c === 'string' ? c : c.textContent)).join(''); }
}
globalThis.document = { createElement: (t) => new El(t), createElementNS: (_, t) => new El(t) };
const { NAV } = await import('./app-nav.js');
const { formatTag, altFormatTag } = await import('./ui.js');
const { groupFormat } = await import('./views/downloads.js');
const tag = (f, p) => { const e = formatTag(f, p); return e ? e.className + ':' + e.textContent : null; };
const alt = (f) => { const e = altFormatTag(f); return e ? e.className + ':' + e.textContent : null; };
const gf = (items) => JSON.stringify(groupFormat({ items }));
const checks = [
  [NAV.map((x) => x[0]).join(' '), 'catalog channels library downloads history settings'],
  [tag('MKV', true), 'fmt pref:MKV'],
  [tag('AVI, MKV', false), 'fmt:AVI, MKV'],
  [tag('', true), null],
  // Ревью 14А, Important 2: MKV только у другой раздачи фильма — своя метка, чужой формат не синий.
  [alt('MKV'), 'fmt pref:есть MKV'],
  [alt(''), null],
  // Ревью 14А, Important 3: строка раздачи в «Загрузках» — фильм из одного файла и свёрнутый сериал.
  [gf([{ file: 'Фильм.mkv', preferred: true }]), '{"format":"MKV","preferred":true}'],
  [gf([{ file: 'Фильм.avi', preferred: false }]), '{"format":"AVI","preferred":false}'],
  [gf([{ file: 's\e1.avi', preferred: false }, { file: 's\e2.mkv', preferred: true }]), '{"format":"MKV","preferred":true}'],
  [gf([{ file: 'e1.mp4', preferred: false }, { file: 'e2.mp4', preferred: false }]), '{"format":"MP4","preferred":false}'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 14В: «Удалить» на карточке медиатеки — своё через медиатеку, скачанное — как в «Загрузках»;
// «Разрешить доступ» и у папки, куда нельзя писать.
func TestPultLibraryDelete(t *testing.T) {
	node := lookNode(t)
	script := `
globalThis.Node = class {};
const { deleteRequest } = await import('./views/library-card.js');
const { grantView } = await import('./views/folders.js');
const own = deleteRequest({ unit: 7, hash: 'lib-7', path: 'D:\\Films\\Фильм.mkv', source: 'Фильмы' });
const dl = deleteRequest({ unit: 9, hash: 'abcdef', path: 'D:\\Films\\X [abcdef12]', source: 'Скачано' });
const g = grantView({ local: true }, { path: 'D:\\Films', problem: 'no_write' }, 8090);
const checks = [
  [own.path, '/library/units/7'],
  [own.text.includes('Фильм.mkv'), true],
  [dl.path, '/downloads/abcdef'],
  [!!(g && g.link && g.link.startsWith('kinodom://grant?')), true],
  [grantView({ local: true }, { path: 'D:\\Films', problem: '' }, 8090), null],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 14Б: переключатель порядка над разделом и цифра порядка на карточке.
func TestPultCatalogOrder(t *testing.T) {
	node := lookNode(t)
	script := `
const { orderLinks, orderStat, readOrderMemory, orderParams } = await import('./views/catalog.js');
const op = (...a) => JSON.stringify(orderParams(...a));
const os = [{ id: 'seeders', name: 'Раздающие' }, { id: 'new', name: 'Новые' }];
const ls = orderLinks(os, 'new', '#/catalog/rutor/12');
const st = (e, o) => { const x = orderStat(e, o); return x ? x.text : null; };
const checks = [
  [ls.length, 2],
  [ls[1].on && !ls[0].on, true],
  [ls[0].href, '#/catalog/rutor/12?order=seeders'],
  [ls[1].name, 'Новые'],
  [st({ seeders: 5 }, 'seeders'), null],
  [st({ leechers: 7 }, 'leechers'), '7'],
  [st({ downloads: 23992 }, 'downloads'), '23\u00a0992'],
  [st({ downloads: 0 }, 'downloads'), null],
  [st({ added: '2026-09-30T12:00:00Z' }, 'new'), '30.09'],
  [st({}, 'new'), null],
  [st({ leechers: 3 }, undefined), null],
  // Ревью 14Б: память выбора — с умолчанием, при котором выбрали (старая память — просто строка).
  [JSON.stringify(readOrderMemory('{"o":"new","d":"seeders"}')), '{"o":"new","d":"seeders"}'],
  [JSON.stringify(readOrderMemory('leechers')), '{"o":"leechers","d":""}'],
  [JSON.stringify(readOrderMemory(null)), '{"o":"","d":""}'],
  // Порядок запроса: показанный (следующие порции) → из адреса → из памяти с since → умолчание сервера.
  [op('', { o: 'new', d: 'seeders' }, 'leechers'), '{"order":"leechers"}'],
  [op('downloads', { o: 'new', d: 'seeders' }, ''), '{"order":"downloads"}'],
  [op('', { o: 'new', d: 'seeders' }, ''), '{"order":"new","since":"seeders"}'],
  [op('', { o: 'new', d: '' }, ''), '{"order":"new"}'],
  [op('', { o: '', d: '' }, ''), '{}'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 14Г: карточка «Кинопоиска» ведёт в поиск по названию и году (кодирование — Review Focus 4); строка
// карточки — год и оценки, пустые части пропускаются.
func TestPultKPCatalog(t *testing.T) {
	node := lookNode(t)
	script := `
const { filmSearchHref, filmLine, kpRestoreCount } = await import('./views/kpcat.js');
const here = '#/catalog/kinopoisk/films-ru?order=kp';
const checks = [
  // Ревью 14Г: сериал — без года (идущий сезон новее первого, а трекерам нужны все слова запроса).
  [filmSearchHref({ id: 7, title: 'Гангстерленд', year: 2025, type: 'TV_SERIES' }), '#/search?q=' + encodeURIComponent('Гангстерленд') + '&kp=7'],
  [filmSearchHref({ id: 8, title: 'Сериал', year: 2024, type: 'MINI_SERIES' }), '#/search?q=' + encodeURIComponent('Сериал') + '&kp=8'],
  // Ревью 14Г: возврат на место — столько карточек, сколько было на этой странице.
  [kpRestoreCount({ at: here, count: 72 }, here), 72],
  [kpRestoreCount({ at: '#/catalog/kinopoisk/docs', count: 72 }, here), 0],
  [kpRestoreCount(null, here), 0],
  [filmSearchHref({ id: 5, title: 'Мастер и Маргарита: «Тест»', year: 2024 }), '#/search?q=' + encodeURIComponent('Мастер и Маргарита: «Тест» 2024') + '&kp=5'],
  [filmSearchHref({ id: 6, title: 'Без года', year: 0 }), '#/search?q=' + encodeURIComponent('Без года') + '&kp=6'],
  [filmLine({ year: 2024, kinopoisk: 8.1, imdb: 0 }), '2024 · КП 8,1'],
  [filmLine({ year: 0, kinopoisk: 0, imdb: 7.25 }), 'IMDb 7,3'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 14Д: «Смотреть» источника — вид плеера по источнику, строка статистики; библиотеки плеера и их
// лицензии — в пульте.
func TestPultPreview(t *testing.T) {
	// mpegts.js.LICENSE.txt — уведомления MIT вшитого в сборку кода, на него ссылается первая строка mpegts.js (ревью 14Д, п. 19).
	for _, f := range []string{"vendor/hls.light.min.js", "vendor/hls.js-LICENSE", "vendor/mpegts.js", "vendor/mpegts.js-LICENSE", "vendor/mpegts.js.LICENSE.txt"} {
		if _, err := fs.Stat(Static, f); err != nil {
			t.Errorf("нет %s", f)
		}
	}
	node := lookNode(t)
	script := `
const { playerKind, previewStats, playerError, frameNote, playerSession, nativeError, hlsWay } = await import('./views/preview.js');
const { logoFileError, fileFieldShown } = await import('./views/settings-unrecognized.js');
const { pickLabel } = await import('./views/channel-settings.js');
const text = (e) => (e ? e.what + ': ' + e.text : null);
const binary = { get responseText() { throw new Error('responseType arraybuffer'); } };
// Ревью 14Д, п. 3: окно закрыли, пока грузилась библиотека, — плеер разбирается, как только подключится.
let destroyed = 0;
let resolve;
const early = playerSession(new Promise((r) => (resolve = r)));
early.close();
resolve({ destroy: () => destroyed++ });
const earlyReady = await early.ready;
let destroyed2 = 0;
const late = playerSession(Promise.resolve({ destroy: () => destroyed2++ }));
const lateReady = await late.ready;
late.close();
late.close();
const checks = [
  [destroyed, 1],
  [earlyReady, null],
  [lateReady !== null && destroyed2, 1],
  // Ревью 14Д, п. 4: ошибки плеера — словами; «браузер не умеет» — не «нет картинки».
  [playerError('hls', { fatal: false, type: 'networkError' }), null],
  [text(playerError('hls', { fatal: true, type: 'networkError', response: { code: 502 }, networkDetails: { responseText: '{"error":"источник ответил 404"}' } })),
    'net: Источник не открылся: источник ответил 404'],
  [text(playerError('hls', { fatal: true, type: 'networkError', response: { code: 502 }, networkDetails: binary })), 'net: Источник не открылся (ответ 502)'],
  [text(playerError('hls', { fatal: true, type: 'networkError' })), 'net: Источник не открылся'],
  [text(playerError('hls', { fatal: true, type: 'mediaError', details: 'manifestIncompatibleCodecsError' })), 'codec: Браузер не показывает этот поток'],
  [text(playerError('hls', { fatal: true, type: 'muxError', details: 'fragParsingError' })), 'codec: Браузер не показывает этот поток'],
  [text(playerError('mpegts', 'NetworkError', 'HttpStatusCodeInvalid', { code: 504, msg: 'Gateway Timeout' })), 'net: Источник не открылся (ответ 504)'],
  [text(playerError('mpegts', 'MediaError', 'MediaMSEError', {})), 'codec: Браузер не показывает этот поток'],
  // Свой HLS браузера (Chrome 154, WebView ТВ) молчит, почему остановился: список спрашивается ещё раз.
  [text(nativeError(4, 'MEDIA_ELEMENT_ERROR: Format error', 502, '{"error":"источник не отвечает"}')), 'net: Источник не открылся: источник не отвечает'],
  [text(nativeError(4, 'DEMUXER_ERROR_NO_SUPPORTED_STREAMS', 200, '#EXTM3U')), 'codec: Браузер не показывает этот поток'],
  [text(nativeError(3, '', 200, '#EXTM3U')), 'codec: Браузер не показывает этот поток'],
  [text(nativeError(2, 'network', 200, '#EXTM3U')), 'net: Источник не открылся'],
  [text(nativeError(4, '', 0, '')), 'net: Источник не открылся'],
  // Свой HLS Chrome 154 не разбирает часть потоков, которые играет hls.js (bipbop 4x3, вживую 14Д): hls.js — везде,
  // где есть MSE; свой — только без MSE (iPhone).
  [hlsWay(true, true), 'hls.js'],
  [hlsWay(true, false), 'hls.js'],
  [hlsWay(false, true), 'native'],
  [hlsWay(false, false), null],
  [frameNote('data:image/jpeg;base64,AAAA'), null],
  [frameNote('none'), 'нет картинки'],
  [frameNote('codec'), 'браузер не показывает этот поток'],
  [frameNote('dash'), 'DASH в браузере не показывается'],
  [frameNote('error'), 'источник не открылся'],
  // Ревью 14Д, п. 10: фото с телефона — отказ до отправки.
  [logoFileError({ size: 3 << 20 }), 'логотип больше 1 МБ'],
  [logoFileError({ size: 50000 }), ''],
  [logoFileError(undefined), ''],
  // Ревью 14Д, п. 16: свой канал в поиске «Назначить» помечен.
  [pickLabel({ key: 'my-1', name: 'НТВ', own: true }), 'НТВ · свой'],
  [pickLabel({ key: 'ntv', name: 'НТВ' }), 'НТВ'],
  // Ревью 14Д, п. 12: в приложении поле файла — только если есть чем выбрать (старое приложение и ТВ без выбора файлов — нет).
  [fileFieldShown(null), true],
  [fileFieldShown({}), false],
  [fileFieldShown({ canPickFiles: () => false }), false],
  [fileFieldShown({ canPickFiles: () => true }), true],
  // Ревью 15Г, п. 8: файл плейлиста — любой тип: на ТВ, где выбрать можно только картинку, кнопки «Файлом» нет.
  [fileFieldShown({ canPickFiles: (t) => t === 'image/*' }, '*/*'), false],
  [fileFieldShown({ canPickFiles: (t) => t === 'image/*' }), true],
  [playerKind({ kind: 'hls', url: 'http://x/a' }), 'hls'],
  [playerKind({ kind: 'live', url: 'http://x/a' }), 'ts'],
  [playerKind({ kind: 'dash', url: 'http://x/a' }), 'dash'],
  [playerKind({ kind: '', url: 'http://x/live/index.m3u8?token=1' }), 'hls'],
  [playerKind({ kind: '', url: 'http://x/manifest.mpd' }), 'dash'],
  [playerKind({ kind: '', url: 'http://x/stream' }), 'ts'],
  [previewStats({ w: 1920, h: 1080, firstMs: 1200, stalls: 0 }), '1920×1080 · 1,2 с до кадра · подвисаний 0'],
  [previewStats({ w: 0, h: 0, firstMs: 0, stalls: 2, latency: 8.4 }), 'подвисаний 2 · задержка 8 с'],
  // Ревью 14Д, п. 14: у потока (TS) — запас буфера, а не «задержка».
  [previewStats({ w: 0, h: 0, firstMs: 0, stalls: 0, buffer: 3.4 }), 'подвисаний 0 · запас 3 с'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Замечено на ТВ (0.14): после перехода по вкладке «Каналы / Не распознано» фокус уходил в левое меню — экран
// перерисовывается, нажатая ссылка пропадает. Ключ нажатого переносится на новый экран.
func TestPultCarryFocus(t *testing.T) {
	node := lookNode(t)
	script := `
globalThis.CSS = { escape: (s) => s };
const body = { dataset: {} };
globalThis.document = { body, activeElement: body };
const { carryFocus, keepFocus } = await import('./ui.js');
const el = (key) => ({ dataset: { key }, disabled: false, focus() { document.activeElement = this; } });
const tab = el('tab-unrecognized');
const main = { items: [tab], querySelector(s) { return this.items.find((x) => s === '[data-key="' + x.dataset.key + '"]') || null; }, contains: () => true };
const checks = [];
carryFocus(main, 'tab-unrecognized');
checks.push(['сразу', document.activeElement === tab]);
// Элемента ещё нет (вид дорисует после загрузки) — встанет при ближайшем keepFocus.
document.activeElement = body;
const late = el('watch-1');
main.items = [];
carryFocus(main, 'watch-1');
checks.push(['пока нет', document.activeElement === body]);
main.items = [late];
keepFocus(main, () => {});
checks.push(['после отрисовки', document.activeElement === late]);
// Review Focus 3: ключа нет — фокус не трогаем.
document.activeElement = body;
carryFocus(main, null);
checks.push(['без ключа', document.activeElement === body]);
// Ревью 15Г, п. 1: поле ввода не получает фокус после перехода — на ТВ снова выскочила бы клавиатура.
document.activeElement = body;
const field = Object.assign(el('q'), { tagName: 'INPUT', type: 'text' });
main.items = [field];
carryFocus(main, 'q');
checks.push(['поле ввода', document.activeElement === body]);
// Ревью 15Г, п. 2: отложенный ключ живёт до следующего перехода — переход без ключа его сбрасывает.
main.items = [];
carryFocus(main, 'gone');
carryFocus(main, null);
main.items = [el('gone')];
keepFocus(main, () => {});
checks.push(['сброс отложенного', document.activeElement === body]);
// Вид сам поставил фокус при отрисовке — его не перебиваем.
const own = el('own');
document.activeElement = own;
carryFocus(main, 'watch-1');
checks.push(['свой фокус вида', document.activeElement === own]);
for (const [name, ok] of checks) if (!ok) { console.error(name); process.exitCode = 1; }
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Ревью 14Б: обходной путь «назад» (другие раздачи → раздача → «назад») возвращал в раздел с порядком из памяти,
// а не с тем, что был в адресе: память каталога — вместе с порядком.
func TestPultCatalogMemory(t *testing.T) {
	node := lookNode(t)
	script := `
const { catalogMemory } = await import('./views/catalog.js');
const checks = [
  [catalogMemory('rutor', '12', 'new'), '#/catalog/rutor/12?order=new'],
  [catalogMemory('rutracker', 'c 2', ''), '#/catalog/rutracker/c%202'],
  [catalogMemory('rutor', '12', null), '#/catalog/rutor/12'],
];
for (const [got, want] of checks) if (got !== want) { console.error(got, '≠', want); process.exitCode = 1; }
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Ревью 14В: место в «Загрузках» — по каждому диску, куда качается; после «Удалить» в медиатеке карточка остаётся,
// если у неё другие версии, а «сейчас смотрят» видно.
func TestPultDisksAndAfterDelete(t *testing.T) {
	node := lookNode(t)
	script := `
const { spaceLine, skippedText } = await import('./views/downloads.js');
const { afterDelete } = await import('./views/library-card.js');
const two = { versions: [{ unit: 1 }, { unit: 2 }] };
const one = { versions: [{ unit: 1 }] };
const checks = [
  [spaceLine({ freeBytes: 5e11, disks: [{ volume: 'D:', freeBytes: 5e11 }, { volume: 'E:', freeBytes: 1e9, low: true }] }),
    { free: 'свободно D: 465,7 ГБ · E: 954 МБ', warn: 'Мало места на диске E:' }],
  [spaceLine({ freeBytes: 5e11, disks: [{ volume: 'D:', freeBytes: 5e11 }] }), { free: 'свободно 465,7 ГБ', warn: '' }],
  [spaceLine({ freeBytes: 1e9, lowSpace: true, disks: [{ volume: 'D:', freeBytes: 1e9, low: true }] }), { free: 'свободно 954 МБ', warn: 'Мало места на диске D:' }],
  [spaceLine({ freeBytes: 1e9, lowSpace: true }), { free: 'свободно 954 МБ', warn: 'Мало места на диске' }],
  // Ревью 15Б, Minor 5: мало на двух — «на дисках».
  [spaceLine({ freeBytes: 1e9, disks: [{ volume: 'D:', freeBytes: 1e9, low: true }, { volume: 'E:', freeBytes: 1e9, low: true }] }).warn, 'Мало места на дисках D:, E:'],
  [skippedText({ skipped: 2 }), 'Сейчас смотрят — 2 серии остались'],
  [skippedText({}), ''],
  [afterDelete(two, 1, {}), { go: null, note: '' }],
  [afterDelete(one, 1, {}), { go: '#/library', note: '' }],
  [afterDelete(one, 1, { skipped: 2 }), { go: null, note: 'Сейчас смотрят — 2 серии остались' }],
];
for (const [got, want] of checks) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { console.error(JSON.stringify(got), '≠', JSON.stringify(want)); process.exitCode = 1; }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 14А, задача 3: на странице сериала крупно — какой сезон скачается.
func TestPultSeasonLabel(t *testing.T) {
	node := lookNode(t)
	script := `
const { seasonLabel } = await import('./views/release.js');
const checks = [
  [seasonLabel('S01'), 'Сезон 1'],
  [seasonLabel('S02E01-08'), 'Сезон 2 · серии 1–8'],
  [seasonLabel('S03E05'), 'Сезон 3 · серия 5'],
  [seasonLabel('Сезон: 1, Серии: 1-8 из 10'), 'Сезон 1 · серии 1–8 из 10'],
  [seasonLabel('Сезон: 1-3, Серии: 1-24 из 24'), 'Сезоны 1–3 · серии 1–24 из 24'],
  [seasonLabel('Сезон: 4'), 'Сезон 4'],
  [seasonLabel('S01-S03'), 'Сезоны 1–3'],
  [seasonLabel(''), ''],
  [seasonLabel('02x13 из 13'), 'Сезон 2 · серия 13 из 13'],
  [seasonLabel('1-5 серий из 5'), 'Серии 1–5 из 5'],
  // Ревью 14А, Important 4: «N серий из M» — сколько серий, а не номер последней.
  [seasonLabel('11 серий из 11'), 'Серии 1–11 из 11'],
  [seasonLabel('Сезон 1, 8 серий из 10'), 'Сезон 1 · серии 1–8 из 10'],
  // Rutor: «[01-07 из 08]» — серии без слова «серии» (ревью 14А, п. 5).
  [seasonLabel('01-07 из 08'), 'Серии 1–7 из 8'],
  [seasonLabel('001-270 из 270'), 'Серии 1–270 из 270'],
  [seasonLabel('05 из 10'), 'Серия 5 из 10'],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestPultAppLine(t *testing.T) {
	node := lookNode(t)
	script := `
import { appLine } from './views/settings-status.js';
const app = { version: '0.11.0-abc', versionCode: 5, url: '/app/kinodom.apk', size: 1 };
const checks = [
  ['адрес есть', appLine(app, ['http://192.168.0.26:8090', 'http://172.26.96.1:8090']), 'http://192.168.0.26:8090/app'],
  ['APK нет', appLine(null, ['http://192.168.0.26:8090']), null],
  ['адреса нет', appLine(app, []), null],
];
for (const [name, got, want] of checks) {
  if (got !== want) {
    console.error(name, ':', got, '≠', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Ревью 14Г: цикл импорта catalog.js ↔ kpcat.js (и library.js ↔ library-card.js) работал, но хрупко: модуль,
// загруженный первым, видит соседа недогруженным. В пульте импорты без циклов.
func TestPultNoImportCycles(t *testing.T) {
	re := regexp.MustCompile(`(?m)^\s*(?:import|export)\s[^;]*?\sfrom\s+'(\.[^']+)'`)
	graph := map[string][]string{}
	err := fs.WalkDir(os.DirFS("static"), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") || strings.HasPrefix(p, "vendor/") {
			return err
		}
		b, err := os.ReadFile(path.Join("static", p))
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			graph[p] = append(graph[p], path.Join(path.Dir(p), m[1]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph["views/kpcat.js"]) == 0 {
		t.Fatalf("импорты не разобраны: %v", graph)
	}
	state := map[string]int{} // 1 — на пути, 2 — пройден
	var stack []string
	var visit func(string)
	visit = func(n string) {
		state[n] = 1
		stack = append(stack, n)
		for _, d := range graph[n] {
			switch state[d] {
			case 1:
				t.Errorf("цикл импорта: %s → %s", strings.Join(stack[slices.Index(stack, d):], " → "), d)
			case 0:
				visit(d)
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = 2
	}
	for _, n := range slices.Sorted(maps.Keys(graph)) {
		if state[n] == 0 {
			visit(n)
		}
	}
}

// Ревью 14Г: пустой раздел «Кинопоиска» — правда о нём (ошибка последней попытки или «идёт первое
// обновление»), «обновлён» — по разделу; повторы при подгрузке отсеиваются; запомненный поиск — с фильмом.
func TestPultKPState(t *testing.T) {
	node := lookNode(t)
	script := `
const { kpEmpty, kpUpdatedAt, freshOnly } = await import('./views/kpcat.js');
const { searchMemory } = await import('./views/search.js');
const shown = new Set([1, 2]);
const checks = [
  [kpEmpty({ id: 'docs', count: 0, error: 'Кинопоиск не отвечает' }), { text: 'Кинопоиск не отвечает', error: true }],
  [kpEmpty({ id: 'docs', count: 0 }), { text: 'Каталог ещё пуст — идёт первое обновление', error: false }],
  [kpUpdatedAt({ sections: [{ id: 'a', updatedAt: '2026-10-01T00:00:00Z' }, { id: 'b', updatedAt: null }] }, 'b'), null],
  [kpUpdatedAt({ sections: [{ id: 'a', updatedAt: '2026-10-01T00:00:00Z' }] }, 'a'), '2026-10-01T00:00:00Z'],
  [freshOnly([{ id: 2 }, { id: 3 }, { id: 3 }], shown).map((f) => f.id), [3]],
  [[...shown], [1, 2, 3]],
  [searchMemory('Матрица 1999', 301), '#/search?q=' + encodeURIComponent('Матрица 1999') + '&kp=301'],
  [searchMemory('x', 0), '#/search?q=x'],
];
for (const [got, want] of checks) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { console.error(JSON.stringify(got), '≠', JSON.stringify(want)); process.exitCode = 1; }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 16Б: «Смотреть» в карточке медиатеки — первым в панели под постером (как «Скачать» у раздачи), фокус —
// на нём. panelOrder — что в панели и в каком порядке.
func TestPultCardPanelOrder(t *testing.T) {
	node := lookNode(t)
	script := `
const { panelOrder } = await import('./views/library-card.js');
const checks = [
  [panelOrder({ versions: [{}, {}] }, true), ['watch', 'versions', 'edits']],
  [panelOrder({ versions: [{}] }, false), ['empty', 'versions', 'edits']],
];
for (const [got, want] of checks) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { console.error(JSON.stringify(got), '≠', JSON.stringify(want)); process.exitCode = 1; }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// План 16Б: поиск — кнопкой в шапке; страница без запроса — поле и история списком, с запросом — без истории.
func TestPultSearchHome(t *testing.T) {
	node := lookNode(t)
	script := `
const { searchParts } = await import('./views/search.js');
const hist = [{ query: 'матрица' }, { query: 'дюна' }];
const checks = [
  [searchParts('', hist), { field: true, history: ['матрица', 'дюна'], clear: true, results: false }],
  [searchParts('', []), { field: true, history: [], clear: false, results: false }],
  [searchParts('дюна', hist), { field: true, history: [], clear: false, results: true }],
];
for (const [got, want] of checks) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { console.error(JSON.stringify(got), '≠', JSON.stringify(want)); process.exitCode = 1; }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}
