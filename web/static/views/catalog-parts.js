// Общие части каталога трекеров и «Кинопоиска»: kpcat.js брал их из catalog.js, а тот — его экран; цикл
// импорта работал, но хрупко (ревью 14Г).
import { h, icon } from '../ui.js';

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

// trackerTabs — вкладки каталога: трекеры (со значком, если у трекера проблемы) и «Кинопоиск» (план 14Г).
export function trackerTabs(current, trackers = {}) {
  return [...TRACKERS, ['kinopoisk', 'Кинопоиск']].map(([id, title]) => {
    const bad = trackers[id] && trackers[id].state !== 'ok';
    return h('a', { href: `#/catalog/${id}`, class: id === current ? 'on' : null, 'aria-current': id === current ? 'page' : null, 'data-key': `tab-${id}` },
      title, bad ? icon('warning', 18, 'Есть проблемы') : null);
  });
}
