// Карточка канала (спека этапа 8, раздел 6.2): «Смотреть», ★, «Скрыть канал», «Проверить»; программа
// на сегодня и завтра; источники по порядку с проверками «днём / вечером», «Закрепить первым», «Это
// другой канал»; правка категории, страны и языка. Правки — из домашней сети.
import { h, icon, poll, keepFocus, ago } from '../ui.js';
import { get, put, post } from '../api.js';
import { GRADE, gradeMark, hhmm, logo, watchChannel, toggleFavorite, starButton } from './tvkit.js';

// CATEGORIES — постоянный набор категорий (спека этапа 8, раздел 5.4), как у сервера.
export const CATEGORIES = [
  ['movies', 'Фильмы и сериалы'], ['sports', 'Спорт'], ['science', 'Познавательные'], ['news', 'Новости'], ['kids', 'Детские'],
  ['music', 'Музыка'], ['entertainment', 'Развлекательные'], ['general', 'Общие'], ['religious', 'Религия'], ['adult', '18+'], ['', 'Без категории'],
];

const STATE = { new: 'ещё не проверен', alive: 'работает', silent: 'не отвечает', dead: 'не отвечает давно' };

// dateStr — «2026-09-30» по местному времени устройства, через days дней.
export function dateStr(days, now = new Date()) {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + days);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

export function render(root, r, ctx) {
  const key = r.parts[1];
  const enc = encodeURIComponent(key);
  let alive = true;
  let card = null;
  let error = '';
  let day = 0; // 0 — сегодня, 1 — завтра
  let tomorrow = null;
  let facets = null; // страны и языки для правки меток — из /channels?all=1
  let favorites = [];
  let reassign = 0; // источник, для которого открыт поиск «Это другой канал»
  let found = [];
  let probing = 0; // время нажатия «Проверить»
  // Поле поиска создаётся один раз: карточка перерисовывается при опросе, а набранное не должно пропадать.
  const reInput = h('input', { class: 'input', placeholder: 'Название канала', 'aria-label': 'Название канала', 'data-key': 'reassign-q' });

  const top = h('div');
  const actions = h('div', { class: 'row wrap' });
  const prog = h('section', { class: 'card', 'aria-label': 'Программа' });
  const srcs = h('section', { class: 'card', 'aria-label': 'Источники' });
  const edit = h('section', { class: 'card', 'aria-label': 'Метки' });
  root.append(h('div', { class: 'screen channel' },
    h('a', { class: 'back', href: '#/channels' }, icon('chevron_left', 18), 'Каналы'), top, actions,
    h('div', { class: 'chan-grid' }, prog, h('div', { class: 'chan-side' }, srcs, edit))));

  const cardPoll = poll(async () => {
    try {
      card = await get(`/channels/${enc}`);
      error = '';
    } catch (e) {
      if (e.status === 404) {
        cardPoll.stop();
        top.replaceChildren(h('p', { class: 'error' }, 'Такого канала нет'));
        return;
      }
      error = e.message;
    }
    if (!alive) return;
    draw();
    if (probing && Date.now() - probing > 60000) probing = 0;
  }, 5000);

  async function refreshFavorites() {
    try {
      const list = await get('/channels');
      favorites = list.channels.filter((c) => c.block === 'favorite').map((c) => c.key);
    } catch {
      favorites = [];
    }
  }

  async function act(fn) {
    try {
      await fn();
      error = '';
    } catch (e) {
      error = e.message;
    }
    cardPoll.now();
  }

  function draw() {
    if (!card) return;
    const c = card;
    const g = GRADE[c.grade] || GRADE.unrated;
    keepFocus(root, () => {
      top.replaceChildren(h('div', { class: 'row chan-head' }, logo(c, 'ch-logo big'),
        h('div', { class: 'grow' },
          h('h1', null, c.name),
          h('div', { class: 'muted sub' }, [c.number ? `№ ${c.number}` : null, c.categoryName, c.countryName, c.languageNames.join(', ') || null].filter(Boolean).join(' · ')),
          h('div', { class: 'tags' }, h('span', { class: 'tag' }, gradeMark(c.grade), g.label),
            c.now ? h('span', { class: 'tag' }, `Сейчас: ${c.now.title}`) : null))));
      actions.replaceChildren(
        h('button', { class: 'btn inv big', type: 'button', 'data-key': 'watch', onclick: () => act(() => watchChannel(key, ctx)) }, icon('play_arrow'), 'Смотреть'),
        h('a', { class: 'btn big wide-only', href: `/m3u/channel/${enc}.m3u8`, download: '', 'data-key': 'm3u' }, icon('playlist_play'), '.m3u8'),
        ctx.canEdit ? starButton(c.favorite, () => act(async () => {
          await refreshFavorites();
          await toggleFavorite(favorites, key);
        }), 'star') : null,
        ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': 'hide', onclick: () => act(() => put(`/channels/${enc}`, { hidden: !c.override.hidden })) },
          icon(c.override.hidden ? 'visibility' : 'visibility_off'), c.override.hidden ? 'Вернуть канал' : 'Скрыть канал') : null,
        h('button', { class: 'btn', type: 'button', disabled: !!probing, 'data-key': 'probe', onclick: () => act(async () => {
          await post('/iptv/probe', { channel: key });
          probing = Date.now();
        }) }, icon('network_check'), probing ? 'Проверяется…' : 'Проверить'),
        error ? h('span', { class: 'error' }, error) : null);
      drawProgramme();
      drawSources();
      drawEdit();
    });
  }

  function drawProgramme() {
    const items = day === 0 ? card.programme : tomorrow;
    const now = Date.now();
    const tabs = h('div', { class: 'seg', role: 'tablist' }, [['Сегодня', 0], ['Завтра', 1]].map(([t, d]) =>
      h('label', { class: d === day ? 'on' : null }, t, h('input', { type: 'radio', name: 'day', checked: d === day, 'data-key': `day-${d}`, onchange: () => {
        day = d;
        if (d === 1 && !tomorrow) {
          get(`/channels/${enc}/epg?date=${dateStr(1)}`).then((res) => {
            tomorrow = res.items;
            if (alive) draw();
          }, (e) => {
            error = e.message;
          });
        }
        draw();
      } }))));
    const rows = (items || []).map((p) => {
      const on = new Date(p.start).getTime() <= now && now < new Date(p.stop).getTime();
      return h('div', { class: on ? 'prog on' : 'prog', 'aria-current': on ? 'true' : null }, h('span', { class: 'prog-time' }, hhmm(p.start)), h('span', null, p.title));
    });
    prog.replaceChildren(h('div', { class: 'row' }, h('div', { class: 'h grow' }, 'Программа'), tabs),
      ...(rows.length ? rows : [h('p', { class: 'muted' }, items ? 'Программы нет' : 'Загружается…')]));
  }

  function week(w) {
    const part = (n, good) => (n ? `${good} из ${n}` : '—');
    return `днём ${part(w.day, w.dayGood)} · вечером ${part(w.evening, w.eveningGood)}`;
  }

  function drawSources() {
    const out = [h('div', { class: 'h' }, 'Источники')];
    card.sources.forEach((s, n) => {
      const info = [s.quality || null, s.kind === 'hls' ? 'HLS' : s.kind === 'dash' ? 'DASH' : s.kind === 'live' ? 'поток' : null,
        s.ttfbMs ? `${(s.ttfbMs / 1000).toFixed(1).replace('.', ',')} с до данных` : null, s.ratio ? `запас ${s.ratio.toFixed(1).replace('.', ',')}×` : null].filter(Boolean).join(' · ');
      const row = h('div', { class: s.offered ? 'src' : 'src off' },
        h('div', { class: 'row' }, h('span', { class: 'num' }, String(n + 1)), gradeMark(s.offered ? (s.grade || 'unrated') : 'black'),
          h('span', { class: 'grow strong ellipsis', title: s.name }, s.name || s.playlists.join(', ')),
          s.pinned ? h('span', { class: 'tag' }, icon('push_pin', 16), 'первый') : null),
        h('div', { class: 'muted small' }, [s.playlists.join(', '), info, STATE[s.state] + (s.checkedAt ? `, проверен ${ago(s.checkedAt)}` : '')].filter(Boolean).join(' · ')),
        h('div', { class: 'muted small' }, 'Неделя: ' + week(s.week)),
        s.error ? h('div', { class: 'error' }, s.error) : null);
      if (ctx.canEdit) {
        row.append(h('div', { class: 'row wrap gap10' },
          h('button', { class: 'btn', type: 'button', 'data-key': `pin-${s.id}`, onclick: () => act(() => put(`/channels/${enc}`, { pinnedSource: s.pinned ? null : s.id })) },
            icon('push_pin'), s.pinned ? 'Открепить' : 'Первым'),
          h('button', { class: 'btn', type: 'button', 'data-key': `other-${s.id}`, onclick: () => {
            reassign = reassign === s.id ? 0 : s.id;
            found = [];
            reInput.value = '';
            draw();
            if (reassign) reInput.focus();
          } }, icon('swap_horiz'), 'Это другой канал')));
        if (reassign === s.id) row.append(reassignBox(s));
      }
      out.push(row);
    });
    if (!card.sources.length) out.push(h('p', { class: 'muted' }, 'Источников нет'));
    srcs.replaceChildren(...out);
  }

  function reassignBox(s) {
    const input = reInput;
    const results = h('div', { class: 'picks' }, found.map((c) => h('button', { class: 'btn', type: 'button', 'data-key': `pick-${c.key}`, onclick: () => act(async () => {
      await put(`/iptv/streams/${s.id}`, { channel: c.key });
      reassign = 0;
    }) }, c.name)));
    const form = h('form', { class: 'row gap10', onsubmit: async (e) => {
      e.preventDefault();
      try {
        found = (await get(`/iptv/epg-channels?q=${encodeURIComponent(input.value)}`)).items;
      } catch (err) {
        error = err.message;
      }
      draw();
      const first = srcs.querySelector('[data-key^="pick-"]');
      if (first) first.focus();
    } }, input, h('button', { class: 'btn', type: 'submit', 'data-key': 'reassign-find' }, icon('search'), 'Найти'),
    h('button', { class: 'btn', type: 'button', 'data-key': 'reassign-hide', onclick: () => act(async () => {
      await put(`/iptv/streams/${s.id}`, { hidden: true });
      reassign = 0;
    }) }, icon('visibility_off'), 'Скрыть источник'));
    return h('div', { class: 'reassign' }, form, results);
  }

  function drawEdit() {
    if (!ctx.canEdit) {
      edit.replaceChildren();
      edit.hidden = true;
      return;
    }
    edit.hidden = false;
    if (!facets) {
      facets = { countries: [], languages: [] };
      get('/channels?all=1').then((d) => {
        facets = d;
        if (alive) draw();
      }, () => {});
    }
    const c = card;
    const opt = (v, t, cur) => h('option', { value: v, selected: v === cur }, t);
    const cat = h('select', { class: 'input', 'data-key': 'cat', 'aria-label': 'Категория' }, CATEGORIES.map(([v, t]) => opt(v, t, c.category)));
    const countries = facets.countries.filter((x) => x.id);
    if (c.country && !countries.some((x) => x.id === c.country)) countries.push({ id: c.country, name: c.countryName });
    const country = h('select', { class: 'input', 'data-key': 'country', 'aria-label': 'Страна' },
      opt('', 'Страна не указана', c.country), countries.map((x) => opt(x.id, x.name, c.country)));
    const langs = facets.languages.filter((x) => x.id);
    for (const [i, l] of c.languages.entries()) if (!langs.some((x) => x.id === l)) langs.push({ id: l, name: c.languageNames[i] });
    const lang = h('select', { class: 'input', 'data-key': 'lang', 'aria-label': 'Язык' },
      opt('', 'Язык не указан', c.languages[0] || ''), langs.map((x) => opt(x.id, x.name, c.languages[0] || '')));
    const overridden = c.override.category !== null || c.override.country !== null || c.override.languages !== null;
    edit.replaceChildren(h('div', { class: 'h' }, 'Метки'),
      h('label', { class: 'fld' }, 'Категория', cat), h('label', { class: 'fld' }, 'Страна', country), h('label', { class: 'fld' }, 'Язык', lang),
      h('div', { class: 'row gap10' },
        h('button', { class: 'btn inv', type: 'button', 'data-key': 'save-labels', onclick: () => act(() => put(`/channels/${enc}`,
          { category: cat.value, country: country.value, languages: lang.value ? [lang.value] : [] })) }, icon('save'), 'Сохранить'),
        overridden ? h('button', { class: 'btn', type: 'button', 'data-key': 'reset-labels', onclick: () => act(() => put(`/channels/${enc}`,
          { category: null, country: null, languages: null })) }, 'Как было') : null));
  }

  return () => {
    alive = false;
    cardPoll.stop();
  };
}
