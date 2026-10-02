// Общие части «Медиатеки» и карточки: library-card.js брал их из library.js, а тот — его экран; цикл импорта
// работал, но хрупко (ревью 14Г).
import { h, rating, thumb } from '../ui.js';

// clock — «с 23 мин» до часа, «с 1:02:10» после.
function clock(sec) {
  const s = Math.floor(sec);
  if (s < 3600) return `с ${Math.floor(s / 60)} мин`;
  const pad = (n) => String(n).padStart(2, '0');
  return `с ${Math.floor(s / 3600)}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}`;
}

// continueLabel — что продолжать: «1×05, с 23 мин», «с 1:02:10», «1×05»; нечего уточнять — «Смотреть».
export function continueLabel(c) {
  const ep = c.episode > 0 ? (c.season > 0 ? `${c.season}×${String(c.episode).padStart(2, '0')}` : String(c.episode)) : '';
  const at = c.positionSec > 0 ? clock(c.positionSec) : '';
  return [ep, at].filter(Boolean).join(', ') || 'Смотреть';
}

// libPoster — постер карточки: картинка или тёмный прямоугольник с названием; рейтинг и «дубли».
export function libPoster(c, cls = 'poster') {
  const box = h('div', { class: cls },
    c.rating > 0 ? h('span', { class: 'kp' }, 'КП ' + rating(c.rating)) : null,
    c.dupes ? h('span', { class: 'vars' }, 'есть дубли') : null,
    h('div', { class: 'ptitle' }, c.title || ''));
  if (c.poster) {
    const img = h('img', { src: cls.includes('big') ? c.poster : thumb(c.poster), alt: '', loading: 'lazy', decoding: 'async' });
    img.addEventListener('load', () => box.classList.add('has-img'));
    img.addEventListener('error', () => img.remove());
    box.prepend(img);
  }
  return box;
}
