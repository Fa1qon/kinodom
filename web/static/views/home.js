// Главный экран: быстрый вход в продолжение просмотра, новые серии и активные загрузки.
// Данные уже существуют в API медиатеки, подписок и торрентов — экран только собирает их
// в один TV-first маршрут.
import { h, fill, icon, keepFocus, poll, day } from '../ui.js';
import { get } from '../api.js';
import { libPoster, continueLabel } from './library-parts.js';
import { playFile } from './library-card.js';
import { poster } from './catalog.js';
import { updateLabel } from './updates.js';

export function render(root, r, ctx) {
  let alive = true;
  let library = null;
  let updates = null;
  let downloads = null;
  let newReleases = {};
  let newTab = 'our-series';
  let error = '';
  let busy = null;
  const body = h('div', { class: 'home-body' });
  root.append(h('div', { class: 'screen home' }, h('h1', null, 'Главная'), body));

  const refresh = poll(async () => {
    const results = await Promise.allSettled([
      get('/library'), get('/updates'), get('/downloads'),
      get('/catalog/new?group=our-series&limit=10'), get('/catalog/new?group=our-film&limit=10'),
      get('/catalog/new?group=foreign-series&limit=10'), get('/catalog/new?group=foreign-film&limit=10'),
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
    if (alive) draw();
  }, 15000);

  function draw() {
    if (!library && !updates && !downloads && !Object.keys(newReleases).length) {
      fill(body, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => fill(body,
      error ? h('p', { class: 'error' }, error) : null,
      dashboardStats(),
      continueSection(),
      newReleasesSection(),
      quickLinks(),
      updatesSection(),
      !((library && library.continue && library.continue.length) || (updates && updates.length) || (downloads && downloads.items && downloads.items.length) ||
        Object.values(newReleases).some((x) => x && x.entries && x.entries.length))
        ? h('p', { class: 'empty' }, 'Здесь появятся фильмы, серии и загрузки') : null));
  }

  function section(title, content, key) {
    if (!content) return null;
    return h('section', { class: 'home-section', 'aria-label': title, 'data-key': key }, h('div', { class: 'row' }, h('h2', { class: 'grow' }, title)), content);
  }

  function continueSection() {
    const items = library && Array.isArray(library.continue) ? library.continue : [];
    if (!items.length) return null;
    const featured = items[0];
    const resume = (c) => h('button', {
      class: 'cont', type: 'button', 'data-key': `continue-${c.card.key}`, disabled: busy === c.file,
      'aria-label': `Продолжить: ${c.card.title}, ${continueLabel(c)}`,
      onclick: () => playContinue(c),
    }, libPoster(c.card, 'poster small'), h('span', { class: 'etitle' }, c.card.title), h('span', { class: 'muted small' }, continueLabel(c)));
    return h('section', { class: 'home-section home-continue', 'aria-label': 'Продолжить просмотр', 'data-key': 'continue' },
      h('div', { class: 'home-section-head' }, h('div', null, h('span', { class: 'eyebrow' }, 'ВЕРНУТЬСЯ К ПРОСМОТРУ'), h('h2', null, 'Продолжить просмотр')),
        h('span', { class: 'home-count' }, `${items.length} ${items.length === 1 ? 'фильм' : 'фильмов'}`)),
      h('div', { class: 'home-hero' },
        h('div', { class: 'home-hero-art' }, libPoster(featured.card, 'poster hero-poster')),
        h('div', { class: 'home-hero-copy' },
          h('span', { class: 'eyebrow' }, featured.card.type === 'SERIES' ? 'СЕРИАЛ' : 'ФИЛЬМ'),
          h('h3', null, featured.card.title),
          h('p', { class: 'home-hero-meta' }, continueLabel(featured)),
          h('p', { class: 'muted' }, featured.episode > 0 ? `Серия ${featured.episode}${featured.season > 0 ? ` · сезон ${featured.season}` : ''}` : 'Продолжить с последнего места'),
          h('button', { class: 'btn inv big', type: 'button', 'data-key': `hero-${featured.card.key}`, 'data-nav-main': true, disabled: busy === featured.file, onclick: () => playContinue(featured) }, icon('play_arrow'), 'Продолжить'))),
      items.length > 1 ? h('div', { class: 'cont-list' }, items.slice(1, 9).map(resume)) : null);
  }

  async function playContinue(c) {
    busy = c.file;
    draw();
    try { await playFile(c.file, ctx, false, c.episode > 0); }
    catch (e) { error = e.message; }
    busy = null;
    if (alive) draw();
  }

  function dashboardStats() {
    const cont = library && Array.isArray(library.continue) ? library.continue.length : 0;
    const fresh = Array.isArray(updates) ? updates.length : 0;
    const all = downloads && Array.isArray(downloads.items) ? downloads.items : [];
    const active = all.filter((d) => d.state === 'watching' || d.state === 'downloading').length;
    return h('div', { class: 'home-stats', 'aria-label': 'Сводка' },
      stat('Продолжить', cont, '#/library', 'play_arrow'), stat('Новые серии', fresh, '#/updates', 'notifications'), stat('Активные загрузки', active, '#/downloads', 'download'));
  }

  function stat(title, value, href, glyph) {
    return h('a', { class: 'home-stat', href, 'data-key': `stat-${title}` }, h('span', { class: 'home-stat-icon' }, icon(glyph)), h('span', { class: 'home-stat-value' }, String(value)), h('span', { class: 'home-stat-title' }, title));
  }

  function quickLinks() {
    return h('div', { class: 'home-quick', 'aria-label': 'Быстрые действия' },
      h('a', { class: 'home-quick-link', href: '#/catalog', 'data-key': 'quick-catalog' }, icon('video_library'), h('span', null, 'Каталог'), h('span', { class: 'muted small' }, 'Найти фильм или сериал')),
      h('a', { class: 'home-quick-link', href: '#/library', 'data-key': 'quick-library' }, icon('video_library'), h('span', null, 'Медиатека'), h('span', { class: 'muted small' }, 'Скачанное и папки')),
      h('a', { class: 'home-quick-link', href: '#/downloads', 'data-key': 'quick-downloads' }, icon('download'), h('span', null, 'Загрузки'), h('span', { class: 'muted small' }, 'Очередь и место')));
  }

  function newReleasesSection() {
    const tabs = [
      ['our-series', 'Наши сериалы'], ['our-film', 'Наше кино'],
      ['foreign-series', 'Зарубежные сериалы'], ['foreign-film', 'Зарубежное кино'],
    ];
    const active = newReleases[newTab];
    const entries = active && Array.isArray(active.entries) ? active.entries : [];
    return h('section', { class: 'home-section home-new', 'aria-label': 'Новинки', 'data-key': 'new-releases' },
      h('div', { class: 'home-section-head' }, h('h2', null, 'Новинки')),
      h('div', { class: 'home-tabs', role: 'tablist', 'aria-label': 'Категории новинок' }, tabs.map(([key, title]) => h('button', {
        class: newTab === key ? 'home-tab on' : 'home-tab', type: 'button', role: 'tab', 'aria-selected': newTab === key,
        'data-key': `new-tab-${key}`, onclick: () => { newTab = key; draw(); },
      }, title))),
      entries.length ? newRail(entries) : h('p', { class: 'muted' }, 'В этой категории пока нет новых раздач'));
  }

  function newRail(entries) {
    const items = Array.isArray(entries) ? entries : [];
    return h('div', { class: 'home-rail' }, items.slice(0, 10).map((e) => {
      const name = e.name || e.title || 'Без названия';
      const meta = [e.year || null, e.quality || null, e.format || null, e.season || null].filter(Boolean).join(' · ');
      const added = e.added ? day(e.added) : null;
      return h('a', { class: 'home-new-card', href: `#/release/${e.id}`, 'data-key': `new-${e.id}`, 'aria-label': `${name}${meta ? `, ${meta}` : ''}` },
        poster(e, name), h('span', { class: 'etitle' }, name), meta ? h('span', { class: 'muted small ellipsis' }, meta) : null,
        added ? h('span', { class: 'muted small' }, `Добавлено: ${added}`) : null);
    }));
  }

  function updatesSection() {
    const items = Array.isArray(updates) ? updates : [];
    if (!items.length) return null;
    return section('Новые серии', h('div', { class: 'home-list' }, items.slice(0, 6).map((u) => h('div', { class: 'home-item' },
      h('div', { class: 'home-item-main' }, h('span', { class: 'strong ellipsis' }, updateLabel(u)), h('span', { class: 'muted small' }, day(u.at))),
      h('a', { class: 'btn', href: `#/release/${u.releaseId}`, 'data-key': `update-${u.id}` }, icon('play_arrow'), 'Открыть')))), 'updates');
  }

  return () => { alive = false; refresh.stop(); };
}
