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

// drawerSections — разделы для панели по порядку сервера; перед первым разделом группы — её заголовок ({head}).
export function drawerSections(sections) {
  const out = [];
  let group = '';
  for (const s of sections) {
    if (s.group && s.group !== group) out.push({ head: s.groupName || '' });
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

// Флаги перехода из панели: выбрали трекер — на новом экране панель снова открыта; выбрали раздел или порядок —
// фокус на первую карточку новой сетки.
let reopenNext = false;
let focusGridNext = false;

// takeReopen, takeFocusGrid — прочитать и сбросить флаг.
export function takeReopen() {
  const v = reopenNext;
  reopenNext = false;
  return v;
}

export function takeFocusGrid() {
  const v = focusGridNext;
  focusGridNext = false;
  return v;
}

// openFilters — панель слева: «Трекер», «Раздел», «Порядок» (пустой блок не рисуется). trackers, orders —
// [{id, name, href, on, bad?, onPick?}], sections — после drawerSections ({head} — заголовок группы). Пункт — ссылка:
// выбор закрывает панель и переходит. «Вправо», «Назад», Escape — закрыть без выбора.
export function openFilters({ trackers = [], sections = [], orders = [] }) {
  let modal = null;
  const pick = (kind, it) => () => {
    if (kind === 'trk') reopenNext = !it.on;
    else focusGridNext = true;
    if (it.onPick) it.onPick();
    modal.close();
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
    modal.close();
  });
  modal = openModal(box, () => modal.close());
  const focus = box.querySelector('a.drow.on[data-key^="sec-"]') || box.querySelector('a.drow.on') || box.querySelector('a.drow');
  if (focus) {
    focus.focus({ preventScroll: true });
    focus.scrollIntoView({ block: 'center' });
  }
  return modal;
}
