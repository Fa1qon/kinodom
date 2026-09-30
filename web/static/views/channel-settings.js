// Настройки канала (#/channel/<ключ>/settings; отзыв заказчика 2026-09-30): «Скрыть канал»,
// «Проверить»; источники по порядку с проверками «днём / вечером», пометками «основной» и «без звука»,
// кнопками «Сделать основным», «Скрыть» / «Вернуть», «Это другой канал»; правка категории, страны и
// языка. Правки — из домашней сети.
import { h, fill, icon, poll, keepFocus, ago } from '../ui.js';
import { get, put, post } from '../api.js';
import { gradeMark } from './tvkit.js';

// CATEGORIES — постоянный набор категорий (спека этапа 8, раздел 5.4), как у сервера.
export const CATEGORIES = [
  ['movies', 'Фильмы и сериалы'], ['sports', 'Спорт'], ['science', 'Познавательные'], ['news', 'Новости'], ['kids', 'Детские'],
  ['music', 'Музыка'], ['entertainment', 'Развлекательные'], ['general', 'Общие'], ['religious', 'Религия'], ['adult', '18+'], ['', 'Без категории'],
];

const STATE = { new: 'ещё не проверен', alive: 'работает', silent: 'не отвечает', dead: 'не отвечает давно' };

// labelPatch — правка меток из черновика: только поля, которые отличаются от меток канала (остальные
// метки не замораживаются правкой). Языки — списком (хвост Х31): порядок не важен, пустой — «Язык не
// указан».
export function labelPatch(c, d) {
  const p = {};
  if (d.category !== c.category) p.category = d.category;
  if (d.country !== c.country) p.country = d.country;
  const sorted = (a) => [...a].sort().join(',');
  if (sorted(d.langs) !== sorted(c.languages)) p.languages = [...d.langs];
  return p;
}

// sourceGrade — квадрат оценки источника: предлагаемый — его оценка; скрытый вручную и не
// предлагаемый — «скрыт» (он может работать — ⚫ «не отвечает» путал, Х32); остальные — ⚫.
export function sourceGrade(s) {
  if (s.offered) return s.grade || 'unrated';
  return s.hidden ? 'hidden' : 'black';
}

// sourceMarks — пометки источника i: «основной» — его плеер получит первым (выбран вручную или
// первый по проверкам); «скрыт» — скрыт у канала вручную (других рабочих нет — сервер отдаёт его
// плееру запасным); «без звука» — по полной проверке.
export function sourceMarks(s, i) {
  const out = [];
  if (s.hidden) out.push(s.offered ? 'скрыт, но других рабочих нет — плеер получит его' : 'скрыт');
  else if (s.pinned) out.push(s.offered ? 'основной — выбран вручную' : 'выбран основным, но сейчас не отвечает');
  else if (s.offered && i === 0) out.push('основной');
  if (s.audio === false) out.push('без звука');
  return out;
}

// sourceButtons — кнопки источника i: keep — «Оставить основным» (первый, выбран проверками), main —
// «Сделать основным», unpin — «Выбирать автоматически», hide — «Скрыть» (hide-last — недоступна:
// единственный предлагаемый источник, для этого есть «Скрыть канал»), show — «Вернуть», other — «Это
// другой канал».
export function sourceButtons(s, i, sources) {
  if (s.hidden) return ['show'];
  const out = [];
  if (s.pinned) out.push('unpin');
  else if (s.offered) out.push(i === 0 ? 'keep' : 'main');
  out.push(s.offered && sources.filter((x) => x.offered).length === 1 ? 'hide-last' : 'hide');
  out.push('other');
  return out;
}

export function render(root, r, ctx) {
  const key = r.parts[1];
  const enc = encodeURIComponent(key);
  let alive = true;
  let card = null;
  let error = '';
  let facets = null; // страны и языки для правки меток — из /channels?all=1
  let draft = null; // черновик меток: {category, country, langs}; null — как у канала
  let reassign = 0; // источник, для которого открыт поиск «Это другой канал»
  let found = [];
  let probing = 0; // время нажатия «Проверить»
  // Поле поиска создаётся один раз: экран перерисовывается при опросе, а набранное не должно пропадать.
  const reInput = h('input', { class: 'input', name: 'channel', placeholder: 'Название канала', 'aria-label': 'Название канала', 'data-key': 'reassign-q' });

  const back = h('a', { class: 'back', href: `#/channel/${enc}` }, icon('chevron_left', 18), 'Канал');
  const top = h('div');
  const actions = h('div', { class: 'row wrap' });
  const srcs = h('section', { class: 'card', 'aria-label': 'Источники' });
  const edit = h('section', { class: 'card', 'aria-label': 'Метки' });
  root.append(h('div', { class: 'screen channel' }, back, top, actions, h('div', { class: 'chan-grid' }, srcs, edit)));

  const cardPoll = poll(async () => {
    try {
      card = await get(`/channels/${enc}`);
      error = '';
    } catch (e) {
      if (e.status === 404) {
        cardPoll.stop();
        fill(top, h('p', { class: 'error' }, 'Такого канала нет'));
        return;
      }
      error = e.message;
    }
    if (!alive) return;
    draw();
    if (probing && Date.now() - probing > 60000) probing = 0;
  }, 5000);

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
    if (!card) {
      fill(top, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    const c = card;
    keepFocus(root, () => {
      fill(back, icon('chevron_left', 18), c.name);
      fill(top, h('h1', null, 'Настройки канала'),
        ctx.canEdit ? null : h('p', { class: 'muted' }, 'Менять можно только из домашней сети'));
      fill(actions,
        ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': 'hide', onclick: () => act(() => put(`/channels/${enc}`, { hidden: !c.override.hidden })) },
          icon(c.override.hidden ? 'visibility' : 'visibility_off'), c.override.hidden ? 'Вернуть канал в список' : 'Скрыть канал') : null,
        h('button', { class: 'btn', type: 'button', disabled: !!probing, 'data-key': 'probe', onclick: () => act(async () => {
          await post('/iptv/probe', { channel: key });
          probing = Date.now();
        }) }, icon('network_check'), probing ? 'Проверяется…' : 'Проверить источники'),
        error ? h('span', { class: 'error' }, error) : null);
      drawSources();
      drawEdit();
    });
  }

  function week(w) {
    const part = (n, good) => (n ? `${good} из ${n}` : '—');
    return `днём ${part(w.day, w.dayGood)} · вечером ${part(w.evening, w.eveningGood)}`;
  }

  function button(s, id) {
    const change = (patch) => () => act(() => put(`/channels/${enc}`, patch));
    switch (id) {
      case 'keep':
        return h('button', { class: 'btn', type: 'button', 'data-key': `pin-${s.id}`, title: 'Сейчас он первый по проверкам — закрепить, чтобы так и осталось',
          onclick: change({ pinnedSource: s.id }) }, icon('push_pin'), 'Оставить основным');
      case 'main':
        return h('button', { class: 'btn', type: 'button', 'data-key': `pin-${s.id}`, onclick: change({ pinnedSource: s.id }) }, icon('push_pin'), 'Сделать основным');
      case 'unpin':
        return h('button', { class: 'btn', type: 'button', 'data-key': `pin-${s.id}`, title: 'Основным станет лучший по проверкам',
          onclick: change({ pinnedSource: null }) }, icon('push_pin'), 'Выбирать автоматически');
      case 'hide':
      case 'hide-last':
        return h('button', { class: 'btn', type: 'button', 'data-key': `hide-${s.id}`, disabled: id === 'hide-last',
          title: id === 'hide-last' ? 'Единственный источник канала — скройте канал целиком' : 'Плеер его больше не получит',
          onclick: change({ hideSource: s.id }) }, icon('visibility_off'), 'Скрыть');
      case 'show':
        return h('button', { class: 'btn', type: 'button', 'data-key': `hide-${s.id}`, onclick: change({ showSource: s.id }) }, icon('visibility'), 'Вернуть');
      case 'other':
        return h('button', { class: 'btn', type: 'button', 'data-key': `other-${s.id}`, onclick: () => {
          reassign = reassign === s.id ? 0 : s.id;
          found = [];
          reInput.value = '';
          draw();
          if (reassign) reInput.focus();
        } }, icon('swap_horiz'), 'Это другой канал');
    }
    return null;
  }

  function drawSources() {
    const out = [h('div', { class: 'h' }, 'Источники'),
      h('p', { class: 'muted small' }, 'Плеер получает источники по порядку: если основной не откроется, возьмёт следующий.')];
    card.sources.forEach((s, n) => {
      const info = [s.quality || null, s.kind === 'hls' ? 'HLS' : s.kind === 'dash' ? 'DASH' : s.kind === 'live' ? 'поток' : null,
        s.ttfbMs ? `${(s.ttfbMs / 1000).toFixed(1).replace('.', ',')} с до данных` : null, s.ratio ? `запас ${s.ratio.toFixed(1).replace('.', ',')}×` : null].filter(Boolean).join(' · ');
      const marks = sourceMarks(s, n).map((m) => h('span', { class: m === 'без звука' ? 'tag warn-tag' : 'tag' },
        icon(m === 'без звука' ? 'warning' : m.startsWith('скрыт') ? 'visibility_off' : 'push_pin', 16), m));
      const row = h('div', { class: s.offered ? 'src' : 'src off' },
        h('div', { class: 'row' }, h('span', { class: 'num' }, String(n + 1)), gradeMark(sourceGrade(s)),
          h('span', { class: 'grow strong ellipsis', title: s.name }, s.name || s.playlists.join(', '))),
        marks.length ? h('div', { class: 'tags' }, marks) : null,
        h('div', { class: 'muted small' }, [s.playlists.join(', '), info, STATE[s.state] + (s.checkedAt ? `, проверен ${ago(s.checkedAt)}` : '')].filter(Boolean).join(' · ')),
        h('div', { class: 'muted small' }, 'Неделя: ' + week(s.week)),
        s.error ? h('div', { class: 'error' }, s.error) : null);
      if (ctx.canEdit) {
        row.append(h('div', { class: 'row wrap gap10' }, sourceButtons(s, n, card.sources).map((id) => button(s, id))));
        if (reassign === s.id) row.append(reassignBox(s));
      }
      out.push(row);
    });
    if (!card.sources.length) out.push(h('p', { class: 'muted' }, 'Источников нет'));
    fill(srcs, ...out);
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
    } }, input, h('button', { class: 'btn', type: 'submit', 'data-key': 'reassign-find' }, icon('search'), 'Найти'));
    return h('div', { class: 'reassign' }, form, results);
  }

  function drawEdit() {
    if (!ctx.canEdit) {
      fill(edit);
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
    // Черновик переживает опрос раз в 5 с: выбор в списке не сбрасывается, пока не сохранили.
    if (!draft) draft = { category: c.category, country: c.country, langs: [...c.languages] };
    const d = draft;
    const opt = (v, t, cur) => h('option', { value: v, selected: v === cur }, t);
    const pick = (field) => (e) => {
      d[field] = e.target.value;
    };
    const cat = h('select', { class: 'input', name: 'category', 'data-key': 'cat', 'aria-label': 'Категория', onchange: pick('category') },
      CATEGORIES.map(([v, t]) => opt(v, t, d.category)));
    const countries = facets.countries.filter((x) => x.id);
    if (d.country && !countries.some((x) => x.id === d.country)) countries.push({ id: d.country, name: d.country === c.country ? c.countryName : d.country });
    const country = h('select', { class: 'input', name: 'country', 'data-key': 'country', 'aria-label': 'Страна', onchange: pick('country') },
      opt('', 'Страна не указана', d.country), countries.map((x) => opt(x.id, x.name, d.country)));
    const langs = facets.languages.filter((x) => x.id);
    for (const [i, l] of c.languages.entries()) if (!langs.some((x) => x.id === l)) langs.push({ id: l, name: c.languageNames[i] });
    // Языки — флажками (хвост Х31): у канала их бывает несколько; ни одного — «Язык не указан».
    const lang = h('div', { class: 'checks', role: 'group', 'aria-label': 'Языки' }, langs.map((x) =>
      h('label', { class: 'check' }, h('input', { type: 'checkbox', name: 'language', value: x.id, 'data-key': `lang-${x.id}`, checked: d.langs.includes(x.id),
        onchange: (e) => {
          d.langs = e.target.checked ? [...d.langs, x.id] : d.langs.filter((l) => l !== x.id);
        } }), x.name)));
    const overridden = c.override.category !== null || c.override.country !== null || c.override.languages !== null;
    fill(edit, h('div', { class: 'h' }, 'Метки'),
      h('label', { class: 'fld' }, 'Категория', cat), h('label', { class: 'fld' }, 'Страна', country), h('div', { class: 'fld' }, 'Языки', lang),
      h('div', { class: 'row gap10' },
        h('button', { class: 'btn inv', type: 'button', 'data-key': 'save-labels', onclick: () => act(async () => {
          const p = labelPatch(card, draft);
          if (Object.keys(p).length) await put(`/channels/${enc}`, p);
          draft = null;
        }) }, icon('save'), 'Сохранить'),
        overridden ? h('button', { class: 'btn', type: 'button', 'data-key': 'reset-labels', onclick: () => act(async () => {
          await put(`/channels/${enc}`, { category: null, country: null, languages: null });
          draft = null;
        }) }, 'Как было') : null));
  }

  return () => {
    alive = false;
    cardPoll.stop();
  };
}
