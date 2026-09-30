// Управление с пульта телевизора и с клавиатуры: стрелки переводят фокус на ближайший элемент в эту
// сторону, OK (Enter) нажимает, «Назад» — на предыдущий экран. Приложение для ТВ показывает этот
// же пульт (решение заказчика, этап 7b).

// select — выпадающие списки (фильтры каналов, метки): без него стрелками до них не добраться (11b).
const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
const DIRS = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' };

// pick — номер ближайшего прямоугольника из cands в сторону dir от from; -1 — в эту сторону ничего
// нет. Кандидат должен лежать целиком в эту сторону, а влево и вправо — ещё и в том же ряду (хоть
// краем по высоте): с последнего элемента ряда вправо фокус не прыгает наискосок. Счёт —
// расстояние по ходу плюс удвоенный сдвиг вбок: элемент на одной линии важнее более близкого
// наискосок.
export function pick(from, cands, dir) {
  let best = -1;
  let bestScore = Infinity;
  cands.forEach((c, i) => {
    let ahead;
    let side;
    if (dir === 'left' || dir === 'right') {
      ahead = dir === 'right' ? c.left - from.right : from.left - c.right;
      side = Math.max(0, Math.max(from.top, c.top) - Math.min(from.bottom, c.bottom));
    } else {
      ahead = dir === 'down' ? c.top - from.bottom : from.top - c.bottom;
      side = Math.max(0, Math.max(from.left, c.left) - Math.min(from.right, c.right));
    }
    if (ahead < -1) return; // не в эту сторону (−1 — общая рамка соседей)
    if ((dir === 'left' || dir === 'right') && side > 0) return; // другой ряд
    const score = Math.max(0, ahead) + 2 * side;
    if (score < bestScore) {
      bestScore = score;
      best = i;
    }
  });
  return best;
}

// nextColumn — номер ближайшей колонки экрана в сторону dir от колонки from (прямоугольники); -1 — в
// эту сторону колонок нет. Колонка, лежащая под или над from (узкий экран), в сторону не считается.
export function nextColumn(from, cols, dir) {
  let best = -1;
  let bestGap = Infinity;
  cols.forEach((c, i) => {
    const gap = dir === 'right' ? c.left - from.right : from.left - c.right;
    if (gap < -1 || gap >= bestGap) return;
    bestGap = gap;
    best = i;
  });
  return best;
}

// enterColumn — на какой элемент колонки встать: запомненный (с него из колонки ушли), иначе главный
// (data-nav-main), иначе ближайший по высоте к from; -1 — элементов нет.
export function enterColumn(items, from, main, remembered) {
  if (remembered >= 0) return remembered;
  if (main >= 0) return main;
  const mid = (from.top + from.bottom) / 2;
  let best = -1;
  let bestD = Infinity;
  items.forEach((r, i) => {
    const d = Math.abs((r.top + r.bottom) / 2 - mid);
    if (d < bestD) {
      bestD = d;
      best = i;
    }
  });
  return best;
}

// leftFrom — колонка → {el, key} элемента, с которого из неё ушли стрелкой вбок: обратно — на него.
const leftFrom = new WeakMap();

// rememberedIndex — номер элемента, с которого ушли из колонки: сам элемент, а если экран его пересоздал
// (строки серий перерисовываются раз в 1–3 с) — элемент с тем же data-key; -1 — нет (ревью 11b-А).
export function rememberedIndex(items, memo) {
  if (!memo) return -1;
  const i = items.indexOf(memo.el);
  if (i >= 0) return i;
  return memo.key ? items.findIndex((el) => el.dataset && el.dataset.key === memo.key) : -1;
}

// columnJump — стрелка вбок, когда в своём ряду ничего нет (спека 11b, 4.1): переход в соседнюю колонку
// экрана [data-nav-column] (боковая панель раздачи). null — колонок нет или в той стороне пусто.
function columnJump(active, dir, all) {
  const col = active.closest ? active.closest('[data-nav-column]') : null;
  if (!col) return null;
  const cols = [...document.querySelectorAll('[data-nav-column]')]
    .filter((c) => c !== col && c.getClientRects().length > 0 && all.some((el) => c.contains(el)));
  const ci = nextColumn(rectOf(col), cols.map(rectOf), dir);
  if (ci < 0) return null;
  const items = all.filter((el) => cols[ci].contains(el));
  const i = enterColumn(items.map(rectOf), rectOf(active), items.findIndex((el) => el.hasAttribute('data-nav-main')),
    rememberedIndex(items, leftFrom.get(cols[ci])));
  if (i < 0) return null;
  leftFrom.set(col, { el: active, key: (active.dataset && active.dataset.key) || '' });
  return items[i];
}

// focusables — элементы, на которые можно перейти: видимые и не внутри скрытого.
export function focusables(root = document) {
  return [...root.querySelectorAll(FOCUSABLE)].filter((el) => el.getClientRects().length > 0 && !el.closest('[hidden], [inert]'));
}

const rectOf = (el) => {
  const r = el.getBoundingClientRect();
  return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
};

function focusOn(el) {
  el.focus({ preventScroll: true });
  el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

// move — фокус в сторону dir; фокуса ещё нет — на первый элемент экрана. false — идти некуда.
export function move(dir) {
  const all = focusables();
  const active = document.activeElement;
  if (!active || !all.includes(active)) {
    const first = all.find((el) => el.closest('main')) || all[0];
    if (first) focusOn(first);
    return !!first;
  }
  const others = all.filter((el) => el !== active);
  // Вбок из колонки — сначала в своей колонке, потом в соседнюю колонку: иначе липкая панель раздачи
  // перехватывает стрелку у строк основной колонки на её высоте, и «обратно» теряется (ревью 11b-А).
  const side = dir === 'left' || dir === 'right';
  const col = side && active.closest ? active.closest('[data-nav-column]') : null;
  const near = col ? others.filter((el) => col.contains(el)) : others;
  const i = pick(rectOf(active), near.map(rectOf), dir);
  if (i >= 0) {
    focusOn(near[i]);
    return true;
  }
  const jump = dir === 'left' || dir === 'right' ? columnJump(active, dir, others) : null;
  if (!jump) return false;
  focusOn(jump);
  return true;
}

// typing — фокус в поле ввода текста: там стрелки влево и вправо двигают курсор.
function typing(el) {
  return !!el && (el.tagName === 'TEXTAREA' || (el.tagName === 'INPUT' && !['checkbox', 'radio', 'button', 'submit', 'range'].includes(el.type)));
}

// arrowMoves — стрелка dir уводит фокус с el (а не двигает курсор в поле). escaped — поле, из которого
// вышли Escape (хвост Х25): фокус остался на нём, и стрелки снова ходят по экрану.
export function arrowMoves(dir, el, escaped) {
  if (!typing(el) || dir === 'up' || dir === 'down' || escaped === el) return true;
  return dir === 'left' ? el.selectionStart === 0 && el.selectionEnd === 0 : el.selectionEnd === el.value.length;
}

// escaped — поле, из которого вышли Escape; ввод символа или уход фокуса возвращают стрелкам курсор.
let escaped = null;

// initNav — клавиши пульта для всего пульта Kinodom.
export function initNav() {
  document.addEventListener('input', () => { escaped = null; });
  document.addEventListener('focusin', (e) => { if (e.target !== escaped) escaped = null; });
  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const el = document.activeElement;
    const text = typing(el);
    if (e.key === 'Escape' && text && escaped !== el) {
      e.preventDefault(); // из поля — к стрелкам; фокус остаётся на поле, стрелка пойдёт от него
      escaped = el;
      return;
    }
    if (e.key === 'Enter' && el && el.tagName === 'SELECT') {
      e.preventDefault(); // OK пульта открывает список
      try {
        el.showPicker();
      } catch {
        // старый браузер — список откроется пробелом
      }
      return;
    }
    if (e.key === 'Escape' || e.key === 'GoBack' || e.key === 'BrowserBack' || (e.key === 'Backspace' && !text)) {
      if (history.length > 1) {
        e.preventDefault();
        history.back();
      }
      return;
    }
    if (e.key === 'Enter' && el && el.matches('input[type="checkbox"], input[type="radio"]')) {
      e.preventDefault(); // OK пульта отмечает флажок, как пробел
      el.click();
      return;
    }
    const dir = DIRS[e.key];
    if (!dir) return;
    if (!arrowMoves(dir, el, escaped)) return; // курсор ещё двигается внутри поля
    if (move(dir)) e.preventDefault();
  });
}
