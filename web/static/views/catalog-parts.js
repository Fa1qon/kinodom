// Общие части каталога трекеров и «Кинопоиска»: kpcat.js брал их из catalog.js, а тот — его экран; цикл
// импорта работал, но хрупко (ревью 14Г).
import { h, icon, openModal } from '../ui.js';
import { playSound } from '../nav.js';

export const TRACKERS = [['rutracker', 'Rutracker'], ['rutor', 'Rutor']];

// oneAtATime — пока вызов fn идёт, повторный возвращает тот же промис: вторая просьба порции ждёт идущую
// загрузку, а не возвращается сразу (иначе экран считал список пустым — найдено вживую, 11b-А).
export function oneAtATime(fn) {
  let running = null;
  return () => {
    if (!running) running = Promise.resolve(fn()).finally(() => { running = null; });
    return running;
  };
}

// retryDue — порция не пришла, а низ сетки на экране или рядом: прокрутка или «вниз» просят её снова
// (наблюдатель пересечения второй раз не срабатывает, пока низ не ушёл из зоны — ревью 11b-А).
export function retryDue(state, tailTop, viewportH) {
  return !!state.error && !state.loading && state.more && tailTop < viewportH + 600;
}

// orderLinks — переключатель порядка над разделом: ссылки на тот же раздел в каждом порядке.
export function orderLinks(orders, current, base) {
  return (orders || []).map((o) => ({ id: o.id, name: o.name, on: o.id === current, href: `${base}?order=${o.id}` }));
}

// trackerItems — трекеры для панели каталога (план 17А): Rutracker, Rutor, «Кинопоиск»; у трекера с проблемами — значок.
export function trackerItems(current, trackers = {}) {
  return [...TRACKERS, ['kinopoisk', 'Кинопоиск']].map(([id, name]) => ({
    id, name, href: `#/catalog/${id}`, on: id === current, bad: !!(trackers[id] && trackers[id].state !== 'ok'),
  }));
}

// Каталог одной кнопкой (план 17А): над сеткой — сводка «трекер · раздел · порядок», выбор — в панели слева. Раньше
// ряды трекеров, групп, разделов и порядков: 4–5 нажатий от меню до сетки, на ТВ — один ряд постеров.

// filterSummary — текст сводки: непустые части через « · ».
export function filterSummary(parts) {
  return parts.filter(Boolean).join(' · ');
}

// drawerSections — разделы для панели по порядку сервера; перед первым разделом группы — её заголовок ({head}); разделы
// без группы после групп — под «Другие разделы» (ревью 17А: иначе — под заголовком последней группы).
export function drawerSections(sections) {
  const out = [];
  let group = '';
  for (const s of sections) {
    if (s.group && s.group !== group) out.push({ head: s.groupName || '' });
    else if (!s.group && group) out.push({ head: 'Другие разделы' });
    group = s.group || '';
    out.push(s);
  }
  return out;
}

// firstColumn — карточка в первой колонке сетки: «влево» с неё открывает панель.
export function firstColumn(rect, firstRect) {
  return Math.abs(rect.left - firstRect.left) < 2;
}

// filterButton — кнопка-сводка над сеткой.
export function filterButton(text, onOpen) {
  return h('button', { class: 'btn fbtn', type: 'button', 'data-key': 'filters', title: text, onclick: onOpen },
    icon('tune'), h('span', { class: 'btn-label' }, text));
}

// Что сделать после перехода из панели: выбрали трекер — на новом экране панель снова открыта ('reopen'); выбрали
// раздел или порядок — фокус на первую карточку ('grid'). Вместе с адресом перехода: экран забирает это в начале
// своей отрисовки, только на том адресе и один раз (ревью 17А: флаг без адреса висел и срабатывал потом на чужом экране).
let pending = null;

export function setPending(href, kind) {
  pending = { href, kind };
}

// takePending — что сделать на адресе hash (null — ничего); сбрасывается при любом вызове.
export function takePending(hash) {
  const p = pending;
  pending = null;
  return p && p.href === hash ? p.kind : null;
}

// samePlace — пункт панели ведёт на тот же адрес: перехода не будет.
export function samePlace(href, hash) {
  return href === hash;
}

// focusFirstCard — фокус на первую карточку сетки экрана.
export function focusFirstCard() {
  const el = document.querySelector('main .grid .entry');
  if (el) el.focus({ preventScroll: true });
  return !!el;
}

// openFilters — панель слева: «Трекер», «Раздел», «Порядок» (пустой блок не рисуется). trackers, orders —
// [{id, name, href, on, bad?, onPick?}], sections — после drawerSections ({head} — заголовок группы). Пункт — ссылка:
// выбор закрывает панель и переходит. «Вправо», «Назад», Escape — закрыть без выбора.
export function openFilters({ trackers = [], sections = [], orders = [] }) {
  if (document.querySelector('.drawer')) return null; // уже открыта (повторное открытие после перехода — ревью 17А)
  let modal = null;
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    window.removeEventListener('hashchange', close);
    modal.close();
    // Фокус было не на чем вернуть (открыли «влево» после перехода) — на сводку (ревью 17А).
    const a = document.activeElement;
    if (!a || a === document.body) {
      const f = document.querySelector('[data-key="filters"]');
      if (f) f.focus({ preventScroll: true });
    }
  };
  const pick = (kind, it) => (e) => {
    if (it.onPick) it.onPick();
    if (samePlace(it.href, location.hash)) {
      // Тот же адрес — перехода не будет: закрыть и сразу сделать, что сделал бы переход.
      e.preventDefault();
      close();
      if (kind !== 'trk') focusFirstCard();
      return;
    }
    setPending(it.href, kind === 'trk' ? 'reopen' : 'grid');
    close();
  };
  const row = (kind, it) => h('a', {
    class: it.on ? 'drow on' : 'drow', href: it.href, 'data-key': `${kind}-${it.id}`, 'aria-current': it.on ? 'true' : null,
    onclick: pick(kind, it),
  }, h('span', { class: 'ellipsis' }, it.name), it.bad ? icon('warning', 18, 'Есть проблемы') : null);
  const block = (title, items) => (items.length ? [h('div', { class: 'dtitle' }, title), ...items] : []);
  const box = h('aside', { class: 'drawer', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Каталог' },
    ...block('Трекер', trackers.map((t) => row('trk', t))),
    ...block('Раздел', sections.map((s) => (s.head !== undefined ? h('div', { class: 'dhead' }, s.head) : row('sec', s)))),
    ...block('Порядок', orders.length > 1 ? orders.map((o) => row('ord', o)) : []));
  box.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowRight') return;
    e.preventDefault();
    e.stopPropagation();
    playSound('back');
    close();
  });
  modal = openModal(box, close);
  // Касание или щелчок мимо панели и переход по истории («Назад» браузера) — закрыть (ревью 17А: на телефоне и мышью
  // панель было не закрыть без выбора).
  box.parentElement.addEventListener('click', (e) => {
    if (e.target === box.parentElement) close();
  });
  window.addEventListener('hashchange', close);
  const focus = box.querySelector('a.drow.on[data-key^="sec-"]') || box.querySelector('a.drow.on') || box.querySelector('a.drow');
  if (focus) {
    focus.focus({ preventScroll: true });
    focus.scrollIntoView({ block: 'center' });
  }
  return modal;
}
