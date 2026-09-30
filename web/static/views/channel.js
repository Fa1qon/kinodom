// Страница канала (спека этапа 8, раздел 6.2): «Смотреть», ★, .m3u8, программа на сегодня и завтра.
// Источники, «Скрыть канал», «Проверить» и метки — в «Настройках канала» (#/channel/<ключ>/settings),
// на странице их нет (отзыв заказчика 2026-09-30).
import { h, fill, icon, poll, keepFocus } from '../ui.js';
import { get } from '../api.js';
import { GRADE, gradeMark, hhmm, inZone, logo, watchChannel, toggleFavorite, starButton } from './tvkit.js';
import * as settings from './channel-settings.js';

// dateStr — «2026-09-30» через days дней, день — по поясу каналов UTC+offset.
export function dateStr(days, offset, now = new Date()) {
  const t = inZone(now, offset);
  const d = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), t.getUTCDate() + days));
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}-${String(d.getUTCDate()).padStart(2, '0')}`;
}

export function render(root, r, ctx) {
  if (r.parts[2] === 'settings') return settings.render(root, r, ctx);
  const key = r.parts[1];
  const enc = encodeURIComponent(key);
  let alive = true;
  let card = null;
  let error = '';
  let day = 0; // 0 — сегодня, 1 — завтра
  let tomorrow = null;

  const top = h('div');
  const actions = h('div', { class: 'row wrap' });
  const prog = h('section', { class: 'card', 'aria-label': 'Программа' });
  root.append(h('div', { class: 'screen channel' },
    h('a', { class: 'back', href: '#/channels' }, icon('chevron_left', 18), 'Каналы'), top, actions, prog));

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
    if (alive) draw();
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
    const g = GRADE[c.grade] || GRADE.unrated;
    keepFocus(root, () => {
      fill(top, h('div', { class: 'row chan-head' }, logo(c, 'ch-logo big'),
        h('div', { class: 'grow' },
          h('h1', null, c.name),
          h('div', { class: 'muted sub' }, [c.number ? `№ ${c.number}` : null, c.categoryName, c.countryName, c.languageNames.join(', ') || null].filter(Boolean).join(' · ')),
          h('div', { class: 'tags' }, h('span', { class: 'tag' }, gradeMark(c.grade), g.label),
            c.override.hidden ? h('span', { class: 'tag' }, icon('visibility_off', 16), 'скрыт из списка') : null,
            c.now ? h('span', { class: 'tag' }, `Сейчас: ${c.now.title}`) : null))));
      fill(actions,
        h('button', { class: 'btn inv big', type: 'button', 'data-key': 'watch', onclick: () => act(() => watchChannel(key, ctx)) }, icon('play_arrow'), 'Смотреть'),
        ctx.canEdit ? starButton(c.favorite, () => act(() => toggleFavorite(c.favorite, key)), 'star') : null,
        h('a', { class: 'btn big wide-only', href: `/m3u/channel/${enc}.m3u8`, download: '', 'data-key': 'm3u' }, icon('playlist_play'), '.m3u8'),
        h('a', { class: 'btn big', href: `#/channel/${enc}/settings`, 'data-key': 'settings' }, icon('tune'), 'Настройки канала'),
        error ? h('span', { class: 'error' }, error) : null);
      drawProgramme();
    });
  }

  function drawProgramme() {
    const items = day === 0 ? card.programme : tomorrow;
    const now = Date.now();
    const tabs = h('div', { class: 'seg', role: 'tablist' }, [['Сегодня', 0], ['Завтра', 1]].map(([t, d]) =>
      h('label', { class: d === day ? 'on' : null }, t, h('input', { type: 'radio', name: 'day', checked: d === day, 'data-key': `day-${d}`, onchange: () => {
        day = d;
        if (d === 1 && !tomorrow) {
          get(`/channels/${enc}/epg?date=${dateStr(1, card.utcOffset)}`).then((res) => {
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
      return h('div', { class: on ? 'prog on' : 'prog', 'aria-current': on ? 'true' : null },
        h('span', { class: 'prog-time' }, hhmm(p.start, card.utcOffset)), h('span', null, p.title));
    });
    fill(prog, h('div', { class: 'row' }, h('div', { class: 'h grow' }, 'Программа'), tabs),
      ...(rows.length ? rows : [h('p', { class: 'muted' }, items ? 'Программы нет' : 'Загружается…')]));
  }

  return () => {
    alive = false;
    cardPoll.stop();
  };
}
