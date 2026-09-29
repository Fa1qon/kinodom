// Управление с пульта телевизора и с клавиатуры: стрелки переводят фокус на ближайший элемент в эту
// сторону, OK (Enter) нажимает, «Назад» — на предыдущий экран. Приложение для ТВ показывает этот
// же пульт (решение заказчика, этап 7b).

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
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
  const i = pick(rectOf(active), others.map(rectOf), dir);
  if (i < 0) return false;
  focusOn(others[i]);
  return true;
}

// typing — фокус в поле ввода текста: там стрелки влево и вправо двигают курсор.
function typing(el) {
  return !!el && (el.tagName === 'TEXTAREA' || (el.tagName === 'INPUT' && !['checkbox', 'radio', 'button', 'submit', 'range'].includes(el.type)));
}

// initNav — клавиши пульта для всего пульта Kinodom.
export function initNav() {
  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const el = document.activeElement;
    const text = typing(el);
    if (e.key === 'Escape' && text) {
      el.blur(); // из поля — к стрелкам
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
    if (text && (dir === 'left' || dir === 'right')) {
      const edge = dir === 'left' ? el.selectionStart === 0 && el.selectionEnd === 0 : el.selectionEnd === el.value.length;
      if (!edge) return; // курсор ещё двигается внутри поля
    }
    if (move(dir)) e.preventDefault();
  });
}
