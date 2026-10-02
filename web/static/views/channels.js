// «Каналы» (спека этапа 8, разделы 5.5 и 6.2): избранное устройства, федеральные, остальные по
// категориям; вкладки категорий, переключатели страны и языка; в строке — «сейчас и следом», оценка
// проверки и «Смотреть» (★ — на странице канала). Время передач — по поясу каналов из настроек. Список
// опрашивается раз в минуту. В приложении «Смотреть» отдаёт плееру показанный список (спека 13, 4.1).
import { h, fill, icon, poll, store, keepFocus, plural } from '../ui.js';
import { get } from '../api.js';
import { gradeMark, hhmm, progressOf, logo, watchChannel, rememberList } from './tvkit.js';

// UNKNOWN — значение переключателя для «страна / язык не указаны».
export const UNKNOWN = '?';

// filtersFrom — вкладка, страна и язык из адреса; параметра нет — запомненные (saved). Экран пишет в
// адрес все три параметра, даже «Все», иначе запомненная вкладка перебила бы выбор.
export function filtersFrom(q, saved) {
  const pick = (k, def) => (q.has(k) ? q.get(k) : saved[k] ?? def);
  return { tab: pick('tab', 'all') || 'all', country: pick('country', ''), lang: pick('lang', '') };
}

// filterChannels — каналы вкладки и переключателей: tab — all, fav, federal или id категории; country и
// lang — "" (все), код или UNKNOWN.
export function filterChannels(channels, { tab = 'all', country = '', lang = '' }) {
  return channels.filter((c) => {
    if (tab === 'fav' && c.block !== 'favorite') return false;
    if (tab === 'federal' && !(c.number > 0)) return false;
    if (tab !== 'all' && tab !== 'fav' && tab !== 'federal' && c.category !== tab) return false;
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

// CH_PORTION — строк списка за раз (план 16А): 460 каналов разом — 4,5 с заморозки на телевизоре.
export const CH_PORTION = 40;

// flatRows — разделы списка одной лентой: заголовок раздела ({sec}) перед его каналами ({ch}); раздел без
// заголовка — только каналы.
export function flatRows(secs) {
  const out = [];
  for (const s of secs) {
    if (s.title) out.push({ sec: s.title });
    for (const c of s.items) out.push({ ch: c });
  }
  return out;
}

// portionEnd — до какой строки строить следующую порцию: built уже построено, всего total.
export function portionEnd(total, built, portion = CH_PORTION) {
  return Math.min(total, built + portion);
}

// sameKeys — тот же набор каналов в том же порядке: тогда опрос меняет передачи на месте, не перестраивая список.
export function sameKeys(a, b) {
  return a.length === b.length && a.every((c, i) => c.key === b[i].key);
}

export function render(root, r, ctx) {
  let saved = {};
  try {
    saved = JSON.parse(store.get('channels') || '{}');
  } catch {
    saved = {};
  }
  const q = r.query;
  const f = filtersFrom(q, saved);
  store.set('channels', JSON.stringify(f));
  let alive = true;
  let data = null;
  let error = '';
  let shownNow = { list: [], listName: 'Все' }; // показанный список — плееру приложения и странице канала

  const head = h('div', { class: 'row wrap' });
  const filters = h('div', { class: 'filters', role: 'tablist', 'aria-label': 'Категории' });
  const list = h('div', { class: 'ch-list' });
  const tail = h('div', { class: 'ch-tail' }); // низ построенного: подошёл к экрану — следующая порция
  root.append(h('div', { class: 'screen channels' }, head, filters, list, tail));
  let rows = []; // лента показанного списка (flatRows)
  let built = 0; // строк ленты построено
  let drawn = null; // каналы, по которым построен список: тот же набор — опрос меняет передачи на месте
  let drawnError = '';
  const refs = new Map(); // ключ канала → узлы строки, которые меняет опрос

  const go = (patch) => {
    const n = { ...f, ...patch };
    const p = new URLSearchParams({ tab: n.tab, country: n.country, lang: n.lang });
    ctx.go('#/channels?' + p);
  };

  // Следующая порция — когда низ построенного подошёл к экрану (наблюдатель и прокрутка) или фокус встал в одну из
  // трёх последних строк (пульт ТВ: «вниз» дальше построенного).
  const more = () => {
    if (!alive || built >= rows.length) return;
    list.append(...buildRows(built, portionEnd(rows.length, built)));
  };
  const near = () => tail.getBoundingClientRect().top < window.innerHeight + 600;
  const watcher = typeof IntersectionObserver === 'function'
    ? new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) more();
    }, { rootMargin: '600px 0px' })
    : null;
  if (watcher) watcher.observe(tail);
  const onScroll = () => {
    if (near()) more();
  };
  window.addEventListener('scroll', onScroll, { passive: true });
  list.addEventListener('focusin', (e) => {
    const row = e.target.closest && e.target.closest('.ch');
    if (!row) return;
    const all = list.querySelectorAll('.ch');
    if ([...all].slice(-3).includes(row)) more();
  });

  const pollList = poll(async () => {
    try {
      data = await get('/channels');
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }, 60000);

  async function watch(c) {
    try {
      await watchChannel(c.version || c.key, ctx, { ...shownNow, start: c });
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
      const hasFed = data.channels.some((c) => c.number > 0);
      const tabs = [['all', 'Все'], ...(hasFav ? [['fav', 'Избранные']] : []), ...(hasFed ? [['federal', 'Федеральные']] : []),
        ...data.categories.map((c) => [c.id, c.name])];
      shownNow = { list: shown, listName: (tabs.find(([id]) => id === f.tab) || [, 'Все'])[1] };
      fill(filters, ...tabs.map(([id, t]) => h('a', { class: id === f.tab ? 'fil on' : 'fil', role: 'tab', 'aria-selected': String(id === f.tab),
        href: '#', 'data-key': `tab-${id || 'none'}`, onclick: (e) => {
          e.preventDefault();
          go({ tab: id });
        } }, t)));
      // Тот же набор каналов — передачи, полоски и оценки меняются на месте: список не перестраивается
      // (раньше — раз в минуту целиком, заморозка на ТВ; план 16А).
      if (drawn && sameKeys(shown, drawn) && error === drawnError) {
        for (const c of shown) patchRow(c);
        drawn = shown;
        return;
      }
      rows = flatRows(sections(shown, f.tab));
      const want = Math.max(CH_PORTION, built); // набор сменился — построено не меньше, чем было: фокус не теряется
      built = 0;
      refs.clear();
      const out = [];
      if (error) out.push(h('p', { class: 'error' }, error));
      if (!rows.length) out.push(h('p', { class: 'empty' }, data.channels.length ? 'Таких каналов нет' : 'Каналов пока нет'));
      out.push(...buildRows(0, portionEnd(rows.length, 0, want)));
      fill(list, ...out);
      drawn = shown;
      drawnError = error;
    });
    if (near()) more();
  }

  // buildRows — строки ленты с from до to (заголовки разделов и каналы); built — до to.
  function buildRows(from, to) {
    const out = [];
    for (const it of rows.slice(from, to)) out.push(it.sec ? h('h2', { class: 'ch-sec' }, it.sec) : row(it.ch));
    built = to;
    return out;
  }

  function row(c) {
    const r = {
      now: h('span', { class: 'ch-now' }),
      track: h('div', { class: 'track ch-track' }, h('div', { style: { background: 'var(--buffer)' } })),
      next: h('span', { class: 'ch-next muted small' }),
      grade: gradeMark(c.grade),
    };
    refs.set(c.key, r);
    fillRow(r, c);
    return h('div', { class: 'ch' },
      h('a', { class: 'ch-main', href: `#/channel/${encodeURIComponent(c.key)}`, 'data-key': `ch-${c.key}`, onclick: () => rememberList(shownNow.list, shownNow.listName) },
        logo(c),
        h('span', { class: 'ch-num' }, c.number ? String(c.number) : ''),
        h('span', { class: 'ch-text' }, h('span', { class: 'ch-name' }, c.name), r.now, r.track, r.next)),
      r.grade,
      h('button', { class: 'btn', type: 'button', 'data-key': `watch-${c.key}`, 'aria-label': `Смотреть ${c.name}`,
        onclick: () => watch(c) }, icon('play_arrow'), h('span', { class: 'wide-only' }, 'Смотреть')));
  }

  // fillRow — «сейчас», полоска и «следом» строки по данным канала c.
  function fillRow(r, c) {
    const now = c.now;
    const next = c.next;
    r.now.className = now ? 'ch-now' : 'ch-now muted';
    r.now.replaceChildren(...(now ? [h('span', { class: 'muted' }, hhmm(now.start, data.utcOffset)), ' ', now.title] : ['—']));
    r.track.hidden = !now;
    if (now) r.track.firstChild.style.width = `${progressOf(now)}%`;
    r.next.hidden = !next;
    r.next.textContent = next ? `${hhmm(next.start, data.utcOffset)} ${next.title}` : '';
  }

  // patchRow — опрос: у построенной строки — новые передачи и оценка; не построена — ничего.
  function patchRow(c) {
    const r = refs.get(c.key);
    if (!r) return;
    fillRow(r, c);
    const g = gradeMark(c.grade);
    if (g.className !== r.grade.className || g.title !== r.grade.title) {
      r.grade.replaceWith(g);
      r.grade = g;
    }
  }

  return () => {
    alive = false;
    pollList.stop();
    if (watcher) watcher.disconnect();
    window.removeEventListener('scroll', onScroll);
  };
}
