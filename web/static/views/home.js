// Главный экран: быстрый вход в продолжение просмотра, новые серии и активные загрузки.
// Данные уже существуют в API медиатеки, подписок и торрентов — экран только собирает их
// в один TV-first маршрут.
import { h, fill, icon, keepFocus, poll, day } from '../ui.js';
import { get } from '../api.js';
import { libPoster, continueLabel } from './library-parts.js';
import { playFile } from './library-card.js';
import { poster } from './catalog.js';
import { updateLabel } from './updates.js';
import { filmPoster } from './kpcat.js';
import { recLine } from './recs.js';

// NEW — ряды новинок трекеров по разделам.
const NEW = [['our-series', 'Новые наши сериалы'], ['our-film', 'Новое наше кино'], ['foreign-series', 'Новые зарубежные сериалы'], ['foreign-film', 'Новое зарубежное кино']];

export function render(root, r, ctx) {
  let alive = true;
  let library = null;
  let updates = null;
  let downloads = null;
  let newReleases = {};
  let forYou = null; // «Для вас»: null — рекомендации не установлены или не ответили
  let wanted = null; // «Хочу посмотреть»
  let error = '';
  let busy = null;
  const body = h('div', { class: 'home-body' });
  root.append(h('div', { class: 'screen home' }, body));

  const refresh = poll(async () => {
    const results = await Promise.allSettled([
      get('/library'), get('/updates'), get('/downloads'),
      get('/catalog/new?group=our-series&limit=20'), get('/catalog/new?group=our-film&limit=20'),
      get('/catalog/new?group=foreign-series&limit=20'), get('/catalog/new?group=foreign-film&limit=20'),
      get('/recs?limit=20&shown=0'), // shown=0: главная обновляется раз в 15 с — это не «показали»
      get('/recs/wanted'),
    ]);
    const failed = results.slice(0, 3).find((x) => x.status === 'rejected');
    if (failed) error = failed.reason && failed.reason.message ? failed.reason.message : 'Не удалось загрузить главный экран';
    else error = '';
    if (results[0].status === 'fulfilled') library = results[0].value;
    if (results[1].status === 'fulfilled') updates = results[1].value;
    if (results[2].status === 'fulfilled') downloads = results[2].value;
    for (const [i, key] of ['our-series', 'our-film', 'foreign-series', 'foreign-film'].entries()) {
      if (results[i + 3].status === 'fulfilled') newReleases[key] = results[i + 3].value;
    }
    forYou = results[7].status === 'fulfilled' ? results[7].value.items : null;
    wanted = results[8].status === 'fulfilled' ? results[8].value.items : null;
    if (alive) draw();
  }, 15000);

  // draw — главная рядами, как у Lampa: у каждого ряда заголовок и лента карточек вбок (пульт: влево-вправо — по
  // ряду, вверх-вниз — между рядами).
  function draw() {
    if (!library && !updates && !downloads && !Object.keys(newReleases).length) {
      fill(body, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    const lines = [
      continueLine(),
      updatesLine(),
      forYouLine(),
      wantedLine(),
      libraryLine(),
      ...NEW.map(([key, title]) => newLine(key, title)),
    ].filter(Boolean);
    keepFocus(root, () => fill(body,
      error ? h('p', { class: 'error' }, error) : null,
      ...lines,
      lines.length ? null : h('p', { class: 'empty' }, 'Здесь появятся фильмы, серии и загрузки')));
  }

  // line — ряд: заголовок и лента карточек; «Все» (more — адрес) — последней плиткой ленты, как у Lampa: вверх-вниз
  // между рядами фокус идёт по карточкам, не заходя в заголовок.
  function line(key, title, cards, more) {
    if (!cards.length) return null;
    return h('section', { class: 'line', 'aria-label': title, 'data-key': `line-${key}` },
      h('div', { class: 'line-head' }, h('h2', null, title)),
      h('div', { class: 'line-body', 'data-row': '' }, cards,
        more ? h('a', { class: 'line-card line-more', href: more, 'data-key': `line-more-${key}` }, h('span', { class: 'line-more-box' }, icon('chevron_right', 36), 'Все')) : null));
  }

  // card — карточка ряда: постер, название, строка под ним; href — ссылка, иначе onclick.
  function card(key, art, title, sub, href, onclick, label) {
    const kids = [art, h('span', { class: 'etitle' }, title), sub ? h('span', { class: 'muted small ellipsis' }, sub) : null];
    return href
      ? h('a', { class: 'entry line-card', href, 'data-key': key, 'aria-label': label || title }, kids)
      : h('button', { class: 'entry line-card', type: 'button', 'data-key': key, 'aria-label': label || title, onclick }, kids);
  }

  function continueLine() {
    const items = library && Array.isArray(library.continue) ? library.continue : [];
    return line('continue', 'Продолжить просмотр', items.slice(0, 20).map((c) => card(`continue-${c.card.key}`, libPoster(c.card, 'poster'),
      c.card.title, continueLabel(c), null, () => playContinue(c), `Продолжить: ${c.card.title}, ${continueLabel(c)}`)));
  }

  async function playContinue(c) {
    if (busy) return;
    busy = c.file;
    try { await playFile(c.file, ctx, false, c.episode > 0); }
    catch (e) { error = e.message; if (alive) draw(); }
    busy = null;
  }

  // updatesLine — новые серии сериалов, на которые подписаны: карточка ведёт на раздачу.
  function updatesLine() {
    const items = Array.isArray(updates) ? updates : [];
    return line('updates', 'Новые серии', items.slice(0, 20).map((u) => card(`update-${u.id}`,
      h('div', { class: 'poster' }, h('div', { class: 'ptitle' }, updateLabel(u))), updateLabel(u), day(u.at), `#/release/${u.releaseId}`)), '#/updates');
  }

  function forYouLine() {
    if (!forYou) return null;
    return line('for-you', 'Для вас', forYou.map((it) => card(`for-you-${it.film.id}`, filmPoster(it.film), it.film.title, it.reason,
      `#/film/${it.film.id}`, null, `${it.film.title}. ${it.reason}`)), '#/recs');
  }

  function wantedLine() {
    if (!wanted) return null;
    return line('wanted', 'Хочу посмотреть', wanted.slice(0, 20).map((f) => card(`wanted-${f.id}`, filmPoster(f), f.title, recLine(f), `#/film/${f.id}`)),
      '#/recs?tab=want');
  }

  // libraryLine — медиатека: карточки фильмов и сериалов, скачанное — первым.
  function libraryLine() {
    const cards = library && Array.isArray(library.cards) ? library.cards : [];
    return line('library', 'Медиатека', cards.slice(0, 20).map((c) => card(`lib-${c.key}`, libPoster(c, 'poster'), c.title,
      c.downloading ? 'качается' : c.year ? String(c.year) : '', `#/library/${encodeURIComponent(c.key)}`)), '#/library');
  }

  // newLine — новые раздачи раздела: найденный на Кинопоиске фильм ведёт на свою страницу, иначе — раздача.
  function newLine(key, title) {
    const entries = newReleases[key] && Array.isArray(newReleases[key].entries) ? newReleases[key].entries : [];
    return line(`new-${key}`, title, entries.slice(0, 20).map((e) => {
      const name = e.name || e.title || 'Без названия';
      const meta = [e.year || null, e.quality || null, e.season || null].filter(Boolean).join(' · ');
      return card(`new-${e.id}`, poster(e, name), name, meta, e.kp > 0 ? `#/film/${e.kp}` : `#/release/${e.id}`);
    }));
  }

  return () => { alive = false; refresh.stop(); };
}
