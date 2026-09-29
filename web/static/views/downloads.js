// Загрузки — по раздачам: скачанное и то, что качается, серии внутри раздачи, место в папке; удаление —
// из домашней сети (спека этапа 7, разделы 5.5, 6.3 и 10.7).
import { h, icon, size, speed, day, ready, poll, keepFocus, plural, shortNames } from '../ui.js';
import { get, del } from '../api.js';

// STATE — подпись, значок и цвет метки состояния.
const STATE = {
  watching: ['смотрят', 'visibility', 'var(--blue)'],
  downloading: ['качается', 'download', ''],
  queued: ['в очереди', 'schedule', 'var(--muted)'],
  paused: ['на паузе', 'pause', 'var(--yellow)'],
  done: ['скачано', 'check', 'var(--green)'],
};
// ORDER — состояние раздачи, если у серий разные: первое, что есть.
const ORDER = ['watching', 'downloading', 'paused', 'queued', 'done'];
const CONFIRM_FOR = 3000; // «Удалить?» ждёт второго нажатия 3 с

export function render(root, r, ctx) {
  let alive = true;
  let view = null;
  let confirming = ''; // корзина, которая превратилась в «Удалить?»: «hash» — раздача, «hash-i» — серия
  let confirmTimer = 0;
  const open = new Set(); // раскрытые раздачи — остаются раскрытыми при опросе
  const errors = new Map(); // корзина → текст ошибки или «сейчас смотрят»
  const warn = h('div');
  const total = h('div', { class: 'total' });
  const list = h('div', { class: 'dl-list' });
  root.append(h('div', { class: 'screen' }, warn, h('div', { class: 'row' }, h('h1', { class: 'grow' }, 'Загрузки'), total), list));

  const refresh = poll(async () => {
    try {
      view = await get('/downloads');
    } catch (e) {
      list.replaceChildren(h('p', { class: 'error' }, e.message));
      return;
    }
    if (alive) draw();
  }, 2000);
  const onStatus = () => {
    if (view) draw();
  };
  ctx.listeners.add(onStatus);

  function draw() {
    warn.replaceChildren(view.lowSpace ? h('div', { class: 'warn' }, icon('warning'), 'Мало места в папке загрузок') : '');
    total.replaceChildren(size(view.usedBytes), h('span', { class: 'muted' }, ` · свободно ${size(view.freeBytes)}`));
    const groups = groupDownloads(view.items);
    keepFocus(list, () => {
      list.replaceChildren(...(groups.length ? groups.flatMap(group) : [h('p', { class: 'muted' }, 'Пока ничего не скачано')]));
    });
  }

  // group — строка раздачи; у раскрытой — строки серий под ней. Раздача из одного файла — одна строка.
  function group(g) {
    const single = g.items.length === 1;
    const d0 = g.items[0];
    const full = g.release ? g.release.title : d0.file;
    const title = shortTitle(full);
    const lead = g.items.find((d) => d.state === g.state) || d0;
    const key = single ? `${g.hash}-${d0.index}` : g.hash;
    const isOpen = open.has(g.hash);
    const thumb = h('div', { class: 'thumb' });
    if (g.release && g.release.imageKey) thumb.append(h('img', { src: `/img/${g.release.imageKey}`, alt: '', loading: 'lazy' }));
    const rows = [h('div', { class: 'dl' },
      thumb,
      h('div', { class: 'dl-name' },
        g.release ? h('a', { class: 'strong', href: `#/release/${g.release.id}`, title: full, 'data-key': `open-${g.hash}` }, title) : h('span', { class: 'strong' }, title),
        h('span', { class: 'muted small ellipsis', title: single ? d0.file : null }, groupLine(g))),
      stateCell(g.state, lead.readiness, g.percent),
      h('div', { class: 'dl-size' }, size(g.size), g.speed ? h('div', { class: 'muted small' }, speed(g.speed)) : null),
      h('div', { class: 'muted small dl-when' }, when(g.items), errorOf(key)),
      h('div', { class: 'dl-actions' },
        single ? null : h('button', { class: 'sq', type: 'button', 'data-key': `exp-${g.hash}`, 'aria-expanded': String(isOpen),
          'aria-label': isOpen ? 'Свернуть серии' : 'Показать серии', onclick: () => {
            if (isOpen) open.delete(g.hash);
            else open.add(g.hash);
            draw();
          } }, icon(isOpen ? 'expand_more' : 'chevron_right')),
        !ctx.canEdit ? null
          : single ? trash(key, d0.canDelete, 'Удалить файл', () => removeFile(d0))
            : trash(key, g.canDelete, 'Удалить раздачу', () => removeRelease(g))))];
    if (!single && isOpen) {
      const names = shortNames(g.items.map((d) => d.file));
      g.items.forEach((d, k) => rows.push(episode(d, names[k])));
    }
    return rows;
  }

  // episode — серия раскрытой раздачи: имя без общего начала и конца, состояние, размер, своя корзина.
  function episode(d, name) {
    const key = `${d.hash}-${d.index}`;
    return h('div', { class: 'dl child' },
      h('div', { class: 'dl-name' }, h('span', { class: 'ellipsis', title: d.file }, name)),
      stateCell(d.state, d.readiness, d.percent),
      h('div', { class: 'dl-size' }, size(d.size), d.speed ? h('div', { class: 'muted small' }, speed(d.speed)) : null),
      h('div', { class: 'muted small dl-when' }, when([d]), errorOf(key)),
      h('div', { class: 'dl-actions' }, ctx.canEdit ? trash(key, d.canDelete, 'Удалить файл', () => removeFile(d)) : null));
  }

  function stateCell(state, readiness, percent) {
    const [label, ic, color] = STATE[state] || STATE.queued;
    const rd = ready[readiness] || ready.none;
    const c = color || rd.color || 'var(--muted)';
    return h('div', { class: 'dl-state' },
      h('span', { class: 'state', style: { borderColor: c, color: c } }, icon(ic, 16),
        state === 'downloading' ? `${label} · ${percent} %` : label),
      state !== 'done' ? h('div', { class: 'track' }, h('div', { style: { width: `${percent}%`, background: c } })) : null);
  }

  // when — когда открывали последний раз и через сколько удалится (ближайшее из серий).
  function when(items) {
    const opened = items.map((d) => d.lastOpenedAt).filter(Boolean).sort().pop();
    const left = items.map((d) => d.deleteInDays).filter((n) => n !== null && n !== undefined);
    return [opened ? day(opened) : null, left.length ? `удалится через ${Math.min(...left)} дн.` : null]
      .filter(Boolean).map((w) => h('div', null, w));
  }

  function errorOf(key) {
    return errors.has(key) ? h('div', { class: 'error' }, errors.get(key)) : null;
  }

  // trash — корзина: первое нажатие — «Удалить?» на 3 с, второе — удалить. То, что смотрят, удалить
  // нельзя: у раздачи — если смотрят все её серии.
  function trash(key, canDelete, label, remove) {
    if (!canDelete) {
      return h('button', { class: 'sq', type: 'button', disabled: true, 'aria-label': 'Сейчас смотрят — удалить нельзя' }, icon('delete'));
    }
    if (confirming === key) {
      return h('button', { class: 'btn danger', type: 'button', 'data-key': `del-${key}`, onclick: () => run(key, remove) }, 'Удалить?');
    }
    return h('button', { class: 'sq', type: 'button', 'data-key': `del-${key}`, 'aria-label': label, onclick: () => {
      confirming = key;
      clearTimeout(confirmTimer);
      confirmTimer = setTimeout(() => {
        confirming = '';
        if (alive && view) draw();
      }, CONFIRM_FOR);
      draw();
    } }, icon('delete'));
  }

  async function run(key, remove) {
    confirming = '';
    clearTimeout(confirmTimer);
    try {
      const note = await remove();
      if (note) errors.set(key, note);
      else errors.delete(key);
    } catch (e) {
      errors.set(key, e.message);
    }
    if (alive) refresh.now();
  }

  async function removeFile(d) {
    await del(`/downloads/${d.hash}/${d.index}`);
    return '';
  }

  // removeRelease — все файлы раздачи (спека этапа 7, раздел 10.6); серию, которую смотрят, сервер
  // оставляет.
  async function removeRelease(g) {
    const res = await del(`/downloads/${g.hash}`);
    return res && res.skipped ? `Сейчас смотрят — ${plural(res.skipped, 'серия осталась', 'серии остались', 'серий осталось')}` : '';
  }

  return () => {
    alive = false;
    refresh.stop();
    clearTimeout(confirmTimer);
    ctx.listeners.delete(onStatus);
  };
}

// groupDownloads — строки «Загрузок» по раздачам, в порядке первой строки каждой раздачи; серии — по
// имени, как на экране раздачи (номер файла в торренте бывает не по порядку серий). Общее состояние — первое, что есть: смотрят, качается, на паузе, в очереди, скачано;
// процент — скачано от общего размера (спека этапа 7, раздел 10.7).
export function groupDownloads(items) {
  const groups = new Map();
  for (const d of items) {
    if (!groups.has(d.hash)) groups.set(d.hash, { hash: d.hash, release: d.release, items: [] });
    groups.get(d.hash).items.push(d);
  }
  return [...groups.values()].map((g) => {
    g.items.sort((a, b) => a.file.localeCompare(b.file, 'ru', { numeric: true }) || a.index - b.index);
    const total = g.items.reduce((n, d) => n + d.size, 0);
    const done = g.items.reduce((n, d) => n + (d.done || 0), 0);
    return {
      ...g,
      size: total,
      percent: total ? Math.floor((done * 100) / total) : 0,
      state: ORDER.find((s) => g.items.some((d) => d.state === s)) || 'queued',
      speed: g.items.reduce((n, d) => n + (d.speed || 0), 0),
      canDelete: g.items.some((d) => d.canDelete),
    };
  });
}

// groupLine — вторая строка раздачи: сезон, качество и число серий («S01 · WEB-DL 1080p · 8 серий»), у
// фильма из одного файла — имя файла. Сезон и качество различают раздачи одного сериала: без них два
// сезона выглядели одинаково, а корзина удаляет раздачу целиком (финальное ревью 7b).
export function groupLine(g) {
  const r = g.release || {};
  const tail = g.items.length === 1 ? g.items[0].file : plural(g.items.length, 'серия', 'серии', 'серий');
  return [r.season, r.quality, tail].filter(Boolean).join(' · ');
}

// shortTitle — название раздачи без переводов, года и качества: «Динозавры / The Dinosaurs [S01]
// (2026) WEB-DL» → «Динозавры».
export function shortTitle(t) {
  const cut = t.search(/ \/ | \(| \[/);
  return cut > 0 ? t.slice(0, cut) : t;
}
