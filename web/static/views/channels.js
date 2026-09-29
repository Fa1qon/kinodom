// «Каналы» (спека этапа 8, разделы 5.5 и 6.2): избранное устройства, федеральные, остальные по
// категориям; вкладки категорий, переключатели страны и языка; в строке — «сейчас и следом», оценка
// проверки, ★ и «Смотреть». Список опрашивается раз в минуту.
import { h, fill, icon, poll, store, keepFocus, plural } from '../ui.js';
import { get } from '../api.js';
import { gradeMark, hhmm, progressOf, logo, watchChannel, toggleFavorite, starButton } from './tvkit.js';

// UNKNOWN — значение переключателя для «страна / язык не указаны».
export const UNKNOWN = '?';

// filterChannels — каналы вкладки и переключателей: tab — all, fav или id категории; country и lang —
// "" (все), код или UNKNOWN.
export function filterChannels(channels, { tab = 'all', country = '', lang = '' }) {
  return channels.filter((c) => {
    if (tab === 'fav' && c.block !== 'favorite') return false;
    if (tab !== 'all' && tab !== 'fav' && c.category !== tab) return false;
    if (country && (country === UNKNOWN ? c.country !== '' : c.country !== country)) return false;
    if (lang && (lang === UNKNOWN ? c.languages.length > 0 : !c.languages.includes(lang))) return false;
    return true;
  });
}

// sections — заголовки списка: во «Всех» — «Избранные», «Федеральные», дальше категории (сервер уже
// отдал каналы по порядку); на других вкладках — один список без заголовка.
export function sections(channels, tab) {
  if (tab !== 'all') return channels.length ? [{ title: '', items: channels }] : [];
  const out = [];
  for (const c of channels) {
    const title = c.block === 'favorite' ? 'Избранные' : c.block === 'federal' ? 'Федеральные' : c.categoryName;
    const last = out[out.length - 1];
    if (last && last.title === title) last.items.push(c);
    else out.push({ title, items: [c] });
  }
  return out;
}

export function render(root, r, ctx) {
  let saved = {};
  try {
    saved = JSON.parse(store.get('channels') || '{}');
  } catch {
    saved = {};
  }
  const q = r.query;
  const f = { tab: q.get('tab') || saved.tab || 'all', country: q.get('country') ?? saved.country ?? '', lang: q.get('lang') ?? saved.lang ?? '' };
  store.set('channels', JSON.stringify(f));
  let alive = true;
  let data = null;
  let error = '';
  let busy = '';

  const head = h('div', { class: 'row wrap' });
  const filters = h('div', { class: 'filters', role: 'tablist', 'aria-label': 'Категории' });
  const list = h('div', { class: 'ch-list' });
  root.append(h('div', { class: 'screen channels' }, head, filters, list));

  const go = (patch) => {
    const n = { ...f, ...patch };
    const p = new URLSearchParams();
    if (n.tab !== 'all') p.set('tab', n.tab);
    if (n.country) p.set('country', n.country);
    if (n.lang) p.set('lang', n.lang);
    ctx.go('#/channels' + (p.toString() ? '?' + p : ''));
  };

  const pollList = poll(async () => {
    try {
      data = await get('/channels');
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }, 60000);

  async function star(key, isFavorite) {
    busy = key;
    draw();
    try {
      await toggleFavorite(isFavorite, key);
    } catch (e) {
      error = e.message;
    }
    busy = '';
    pollList.now();
  }

  async function watch(key) {
    try {
      await watchChannel(key, ctx);
    } catch (e) {
      error = e.message;
      draw();
    }
  }

  function select(label, value, facets, key) {
    const opts = [h('option', { value: '' }, label)];
    for (const x of facets) opts.push(h('option', { value: x.id || UNKNOWN, selected: (x.id || UNKNOWN) === value }, `${x.name} · ${x.count}`));
    return h('select', { class: 'input sel', name: key, 'aria-label': label, 'data-key': `sel-${key}`, onchange: (e) => go({ [key]: e.target.value }) }, opts);
  }

  function draw() {
    if (!data) {
      fill(list, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    const shown = filterChannels(data.channels, f);
    const hasFav = data.channels.some((c) => c.block === 'favorite');
    keepFocus(root, () => {
      fill(head, h('h1', { class: 'grow' }, 'Каналы'),
        h('span', { class: 'muted' }, plural(shown.length, 'канал', 'канала', 'каналов')),
        select('Все страны', f.country, data.countries, 'country'),
        select('Все языки', f.lang, data.languages, 'lang'));
      const tabs = [['all', 'Все'], ...(hasFav ? [['fav', 'Избранные']] : []), ...data.categories.map((c) => [c.id, c.name])];
      fill(filters, ...tabs.map(([id, t]) => h('a', { class: id === f.tab ? 'fil on' : 'fil', role: 'tab', 'aria-selected': String(id === f.tab),
        href: '#', 'data-key': `tab-${id || 'none'}`, onclick: (e) => {
          e.preventDefault();
          go({ tab: id });
        } }, t)));
      const secs = sections(shown, f.tab);
      const out = [];
      if (error) out.push(h('p', { class: 'error' }, error));
      if (!secs.length) out.push(h('p', { class: 'empty' }, data.channels.length ? 'Таких каналов нет' : 'Каналов пока нет'));
      for (const s of secs) {
        if (s.title) out.push(h('h2', { class: 'ch-sec' }, s.title));
        out.push(...s.items.map(row));
      }
      fill(list, ...out);
    });
  }

  function row(c) {
    const now = c.now;
    const next = c.next;
    return h('div', { class: 'ch' },
      h('a', { class: 'ch-main', href: `#/channel/${encodeURIComponent(c.key)}`, 'data-key': `ch-${c.key}` },
        logo(c),
        h('span', { class: 'ch-num' }, c.number ? String(c.number) : ''),
        h('span', { class: 'ch-text' },
          h('span', { class: 'ch-name' }, c.name),
          now ? h('span', { class: 'ch-now' }, h('span', { class: 'muted' }, hhmm(now.start)), ' ', now.title) : h('span', { class: 'ch-now muted' }, '—'),
          now ? h('div', { class: 'track ch-track' }, h('div', { style: { width: `${progressOf(now)}%`, background: 'var(--buffer)' } })) : null,
          next ? h('span', { class: 'ch-next muted small' }, `${hhmm(next.start)} ${next.title}`) : null)),
      gradeMark(c.grade),
      ctx.canEdit ? starButton(c.block === 'favorite', () => star(c.key, c.block === 'favorite'), `star-${c.key}`) : null,
      h('button', { class: 'btn', type: 'button', disabled: busy === c.key, 'data-key': `watch-${c.key}`, 'aria-label': `Смотреть ${c.name}`,
        onclick: () => watch(c.key) }, icon('play_arrow'), h('span', { class: 'wide-only' }, 'Смотреть')));
  }

  // canEdit пришёл позже списка — ★ появляется. Только при смене canEdit: «Состояние» опрашивается раз в
  // 15 с, а список на ТВ — около тысячи строк.
  let shownCanEdit = ctx.canEdit;
  const onStatus = () => {
    if (alive && data && ctx.canEdit !== shownCanEdit) {
      shownCanEdit = ctx.canEdit;
      draw();
    }
  };
  ctx.listeners.add(onStatus);
  return () => {
    alive = false;
    pollList.stop();
    ctx.listeners.delete(onStatus);
  };
}
