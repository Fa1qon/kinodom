// «Медиатека» (спека этапа 9, раздел 6.1): «Продолжить просмотр», вкладки категорий, сетка постеров,
// новые сверху; «Не распознано» — вкладка, пока такие есть. При открытии — обход папок в фоне, экран
// обновляется, когда он закончился. Карточка — library-card.js (#/library/<ключ>).
import { h, fill, icon, keepFocus, rating, plural } from '../ui.js';
import { get, post, put } from '../api.js';
import * as card from './library-card.js';

// libraryTabs — вкладки: «Все», категории, «Не распознано» (если есть).
export function libraryTabs(v) {
  const all = v.categories.reduce((n, c) => n + c.count, 0);
  return [{ id: 'all', name: 'Все', count: all }, ...v.categories.map((c) => ({ id: String(c.id), name: c.name, count: c.count })),
    ...(v.unrecognized > 0 ? [{ id: 'unrecognized', name: 'Не распознано', count: v.unrecognized }] : [])];
}

// clock — «с 23 мин» до часа, «с 1:02:10» после.
function clock(sec) {
  const s = Math.floor(sec);
  if (s < 3600) return `с ${Math.floor(s / 60)} мин`;
  const pad = (n) => String(n).padStart(2, '0');
  return `с ${Math.floor(s / 3600)}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}`;
}

// continueLabel — что продолжать: «1×05, с 23 мин», «с 1:02:10», «1×05»; нечего уточнять — «Смотреть».
export function continueLabel(c) {
  const ep = c.episode > 0 ? (c.season > 0 ? `${c.season}×${String(c.episode).padStart(2, '0')}` : String(c.episode)) : '';
  const at = c.positionSec > 0 ? clock(c.positionSec) : '';
  return [ep, at].filter(Boolean).join(', ') || 'Смотреть';
}

// libPoster — постер карточки: картинка или тёмный прямоугольник с названием; рейтинг и «дубли».
export function libPoster(c, cls = 'poster') {
  const box = h('div', { class: cls },
    c.rating > 0 ? h('span', { class: 'kp' }, 'КП ' + rating(c.rating)) : null,
    c.dupes ? h('span', { class: 'vars' }, 'есть дубли') : null,
    h('div', { class: 'ptitle' }, c.title || ''));
  if (c.poster) {
    const img = h('img', { src: c.poster, alt: '', loading: 'lazy', decoding: 'async' });
    img.addEventListener('load', () => box.classList.add('has-img'));
    img.addEventListener('error', () => img.remove());
    box.prepend(img);
  }
  return box;
}

export function render(root, r, ctx) {
  if (r.parts[1]) return card.render(root, r, ctx);
  const tab = r.query.get('cat') || 'all';
  let alive = true;
  let data = null;
  let unrec = null;
  let error = '';
  let timer = 0;
  const forms = new Map(); // поля «Не распознано» по единице: создаются один раз, опрос их не сбрасывает

  const head = h('div', { class: 'row wrap' });
  const tabs = h('nav', { class: 'filters', 'aria-label': 'Категории' });
  const cont = h('section', { class: 'cont-row', 'aria-label': 'Продолжить просмотр' });
  const body = h('div');
  root.append(h('div', { class: 'screen library' }, head, cont, tabs, body));
  post('/library/scan').catch(() => {});

  // load — список; пока идёт обход — раз в 2 с, потом раз в минуту.
  async function load() {
    timer = 0;
    try {
      const q = /^\d+$/.test(tab) ? `?category=${tab}` : '';
      data = await get('/library' + q);
      if (tab === 'unrecognized') unrec = await get('/library/unrecognized');
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (!alive) return;
    draw();
    timer = setTimeout(load, data && data.scan.running ? 2000 : 60000);
  }
  load();

  function draw() {
    if (!data) {
      fill(body, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => {
      fill(head, h('h1', { class: 'grow' }, 'Медиатека'),
        data.scan.running ? h('span', { class: 'muted small' }, 'Обновляется…') : null,
        error ? h('span', { class: 'error' }, error) : null);
      fill(tabs, ...libraryTabs(data).map((t) => h('a', { class: t.id === tab ? 'fil on' : 'fil', href: t.id === 'all' ? '#/library' : `#/library?cat=${t.id}`,
        'aria-current': t.id === tab ? 'page' : null, 'data-key': `tab-${t.id}` }, t.name, h('span', { class: 'muted small' }, ' ' + t.count))));
      fill(cont, ...(tab === 'all' && data.continue.length ? [h('h2', null, 'Продолжить просмотр'),
        h('div', { class: 'cont-list' }, data.continue.map(continueItem))] : []));
      if (tab === 'unrecognized') {
        fill(body, unrecognized());
        return;
      }
      if (!data.cards.length) {
        const empty = data.categories.every((c) => c.count === 0);
        fill(body, h('p', { class: 'empty' }, empty ? 'В медиатеке пока ничего нет' : 'Здесь пусто'),
          empty && ctx.canEdit ? h('a', { class: 'btn', href: '#/settings/library', 'data-key': 'add-folders' }, icon('folder'), 'Добавить папки') : null);
        return;
      }
      fill(body, h('div', { class: 'grid' }, data.cards.map(entry)));
    });
  }

  function entry(c) {
    const marks = [c.year || null, c.downloading ? 'качается' : null,
      c.deleteInDays !== null && c.deleteInDays !== undefined ? `удалится через ${plural(c.deleteInDays, 'день', 'дня', 'дней')}` : null].filter(Boolean);
    return h('a', { class: 'entry', href: `#/library/${encodeURIComponent(c.key)}`, 'data-key': `card-${c.key}` },
      libPoster(c), h('div', { class: 'etitle' }, c.title), marks.length ? h('div', { class: 'muted small' }, marks.join(' · ')) : null);
  }

  function continueItem(c) {
    return h('button', { class: 'cont', type: 'button', 'data-key': `cont-${c.card.key}`, 'aria-label': `Продолжить: ${c.card.title}, ${continueLabel(c)}`,
      onclick: async () => {
        try {
          await card.playFile(c.file, ctx, false, c.episode > 0);
        } catch (e) {
          error = e.message;
          draw();
        }
      } },
    libPoster(c.card, 'poster small'), h('span', { class: 'etitle' }, c.card.title), h('span', { class: 'muted small' }, continueLabel(c)));
  }

  function formOf(u) {
    let f = forms.get(u.unit);
    if (!f) {
      f = {
        link: h('input', { class: 'input', name: 'kinopoisk', placeholder: 'Ссылка на Кинопоиск', 'aria-label': 'Ссылка на Кинопоиск', 'data-key': `kp-${u.unit}` }),
        title: h('input', { class: 'input', name: 'title', placeholder: 'Название', 'aria-label': 'Название', value: u.title, 'data-key': `title-${u.unit}` }),
        year: h('input', { class: 'input year', name: 'year', placeholder: 'Год', 'aria-label': 'Год', inputmode: 'numeric', value: u.year || '', 'data-key': `year-${u.unit}` }),
      };
      forms.set(u.unit, f);
    }
    return f;
  }

  async function act(fn) {
    try {
      await fn();
      error = '';
    } catch (e) {
      error = e.message;
    }
    clearTimeout(timer);
    load();
  }

  function unrecognized() {
    const items = unrec || [];
    if (!items.length) return h('p', { class: 'empty' }, 'Всё распознано');
    return h('div', { class: 'un-list' }, items.map((u) => {
      const f = formOf(u);
      return h('div', { class: 'un' },
        h('div', { class: 'strong' }, u.name),
        h('div', { class: 'muted small' }, [u.category, u.title + (u.year ? ` (${u.year})` : ''), u.path].filter(Boolean).join(' · ')),
        ctx.canEdit ? [
          h('form', { class: 'row gap10', onsubmit: (e) => {
            e.preventDefault();
            act(() => put(`/library/units/${u.unit}`, { kinopoisk: f.link.value }));
          } }, f.link, h('button', { class: 'btn', type: 'submit', 'data-key': `link-${u.unit}` }, icon('link'), 'Привязать')),
          h('form', { class: 'row gap10', onsubmit: (e) => {
            e.preventDefault();
            act(() => put(`/library/units/${u.unit}`, { manual: { title: f.title.value, year: parseInt(f.year.value, 10) || 0 } }));
          } }, f.title, f.year, h('button', { class: 'btn', type: 'submit', 'data-key': `manual-${u.unit}` }, icon('save'), 'Разметить вручную')),
          h('div', { class: 'row gap10' },
            h('button', { class: 'btn', type: 'button', 'data-key': `again-${u.unit}`, onclick: () => act(() => put(`/library/units/${u.unit}`, { search: true })) },
              icon('search'), 'Искать снова'),
            h('a', { class: 'btn', href: `#/library/${encodeURIComponent(u.card)}`, 'data-key': `open-${u.unit}` }, icon('play_arrow'), 'Открыть')),
        ] : null);
    }));
  }

  return () => {
    alive = false;
    clearTimeout(timer);
  };
}
