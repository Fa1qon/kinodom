// Общие помощники пульта: элементы DOM, иконки, числа по-русски, опрос сервера.
import { paths } from './icons.js';

const SVG = 'http://www.w3.org/2000/svg';
// Свойства, которые задаются полем элемента, а не атрибутом.
const PROPS = new Set(['value', 'checked', 'disabled', 'hidden', 'indeterminate', 'selected']);

// h — элемент DOM: h('a', {href: '#/', class: 'btn', onclick: fn}, 'текст', h('span', null, '…')).
// Атрибуты со значением null, undefined и false не ставятся; дети-массивы разворачиваются.
export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (PROPS.has(k)) el[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  append(el, kids);
  return el;
}

function append(el, kids) {
  for (const k of kids) {
    if (k === null || k === undefined || k === false) continue;
    if (Array.isArray(k)) append(el, k);
    else el.append(k instanceof Node ? k : String(k));
  }
}

// pendingFocus — часть экрана → data-key элемента, на который фокус вернуть не удалось: кнопку на время
// действия отключили («Скачать», «Смотреть»). Фокус вернётся при следующей перерисовке, когда она
// снова доступна.
const pendingFocus = new WeakMap();

// keepFocus — перерисовать часть экрана, не сбив фокус пульта: элемент с тем же data-key снова в
// фокусе. Экраны, которые опрашивают сервер, перерисовываются раз в 1–2 с. Фокус ни на чём (body) —
// пробуем отложенный ключ; человек сам ушёл фокусом в другое место — отложенный ключ забыт.
export function keepFocus(root, draw) {
  const active = document.activeElement;
  let key = active && root.contains(active) && active.dataset ? active.dataset.key : null;
  if (!key && (!active || active === document.body)) key = pendingFocus.get(root) || null;
  else if (!key) pendingFocus.delete(root);
  draw();
  if (!key) return;
  const el = root.querySelector(`[data-key="${CSS.escape(key)}"]`);
  if (el && !el.disabled) {
    el.focus({ preventScroll: true });
    pendingFocus.delete(root);
  } else {
    pendingFocus.set(root, key);
  }
}

// offWarn — трекер выключен: адрес не введён (этап 11a). Строка из «Состояния» и путь в «Параметры».
export function offWarn(text) {
  return h('div', { class: 'warn' }, icon('warning'), h('span', { class: 'grow' }, text),
    h('a', { class: 'btn', href: '#/settings/params', 'data-key': 'to-params' }, 'Параметры'));
}

// fileBase64 — файл (плейлист) в base64: изменяющие запросы к API — только JSON.
export function fileBase64(file) {
  return new Promise((resolve, reject) => {
    const rd = new FileReader();
    rd.onload = () => resolve(String(rd.result).replace(/^data:[^,]*,/, ''));
    rd.onerror = () => reject(new Error('файл не читается'));
    rd.readAsDataURL(file);
  });
}

// clear — убрать всё содержимое элемента.
export function clear(el) {
  el.replaceChildren();
}

// fill — заменить содержимое элемента; null, undefined и false пропускаются, массивы разворачиваются,
// как у h() (replaceChildren напечатал бы «null» текстом).
export function fill(el, ...kids) {
  el.replaceChildren();
  append(el, kids);
}

// icon — значок Material. С label — картинка с подписью для экранного диктора, иначе скрыт от него.
export function icon(name, size = 20, label = '') {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('class', 'ic');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'currentColor');
  if (label) {
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', label);
  } else {
    svg.setAttribute('aria-hidden', 'true');
  }
  const p = document.createElementNS(SVG, 'path');
  p.setAttribute('d', paths[name] || '');
  svg.append(p);
  return svg;
}

const comma = (n, digits = 1) => n.toFixed(digits).replace('.', ',');
const MB = 1 << 20;
const GB = 1 << 30;

// size — «17,4 ГБ», «987 МБ».
export function size(bytes) {
  if (!bytes || bytes < 0) return '0 МБ';
  if (bytes >= GB) return `${comma(bytes / GB)} ГБ`;
  if (bytes >= MB) return `${Math.round(bytes / MB)} МБ`;
  return '<1 МБ';
}

// speed — «3,1 МБ/с», «850 КБ/с».
export function speed(bytesPerSec) {
  if (!bytesPerSec || bytesPerSec < 1024) return '0 КБ/с';
  if (bytesPerSec >= MB) return `${comma(bytesPerSec / MB)} МБ/с`;
  return `${Math.round(bytesPerSec / 1024)} КБ/с`;
}

// rating — «8,3».
export function rating(x) {
  return comma(x);
}

// ago — «2 ч назад»; null — «ещё не было».
export function ago(iso) {
  if (!iso) return 'ещё не было';
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60) return 'только что';
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`;
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`;
  return `${Math.floor(s / 86400)} дн. назад`;
}

// day — «сегодня», «вчера», «5 дн. назад».
export function day(iso) {
  const d = new Date(iso);
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  if (d >= start) return 'сегодня';
  const days = Math.ceil((start - d) / 86400000);
  return days === 1 ? 'вчера' : `${days} дн. назад`;
}

// minutes — «~6 мин» до просмотра без остановок.
export function minutes(sec) {
  return `~${Math.max(1, Math.ceil(sec / 60))} мин`;
}

// baseName — имя файла без папок и расширения: «Серия 3. Пресные воды».
export function baseName(p) {
  const name = p.split(/[\\/]/).pop();
  const dot = name.lastIndexOf('.');
  return dot > 0 ? name.slice(0, dot) : name;
}

// plural — «1 пир», «3 пира», «12 пиров».
export function plural(n, one, few, many) {
  const d = n % 10;
  const dd = n % 100;
  if (d === 1 && dd !== 11) return `${n} ${one}`;
  if (d >= 2 && d <= 4 && (dd < 12 || dd > 14)) return `${n} ${few}`;
  return `${n} ${many}`;
}

// shortNames — имена серий без общего начала и конца: «The.Dinosaurs.S01E01.720p.NF…» → «S01E01».
// Слова — части имени между точками, пробелами, «_» и «-». Если у какой-то серии ничего не
// остаётся — полные имена.
export function shortNames(names) {
  const full = names.map(baseName);
  if (full.length < 2) return full;
  const words = full.map((n) => n.split(/[\s._-]+/).filter(Boolean));
  const minLen = Math.min(...words.map((w) => w.length));
  let pre = 0;
  while (pre < minLen && words.every((w) => w[pre] === words[0][pre])) pre++;
  let post = 0;
  while (post < minLen - pre && words.every((w) => w[w.length - 1 - post] === words[0][words[0].length - 1 - post])) post++;
  const short = words.map((w) => w.slice(pre, w.length - post).join(' '));
  return short.every(Boolean) ? short : full;
}

// fileFormat — формат файла по расширению: «MKV»; без расширения — "" (спека этапа 7, раздел 10.7).
export function fileFormat(name) {
  const m = /\.([a-z0-9]{2,4})$/i.exec(name.split(/[\\/]/).pop());
  return m ? m[1].toUpperCase() : '';
}

// ready — цвет и подпись кнопки «Смотреть» по готовности файла (цвет считает сервер).
export const ready = {
  none: { color: '', label: '' },
  wait: { color: 'var(--yellow)', label: 'ещё мало скачано' },
  smooth: { color: 'var(--blue)', label: 'можно смотреть без остановок' },
  done: { color: 'var(--green)', label: 'скачано целиком' },
};

// poll — вызывать fn сразу и потом раз в ms; пока вкладка скрыта — пауза. stop() — остановить,
// now() — вызвать сразу (после действия пользователя).
export function poll(fn, ms) {
  let timer = 0;
  let stopped = false;
  let busy = false;
  const tick = async () => {
    timer = 0;
    if (stopped || document.hidden) return;
    busy = true;
    try {
      await fn();
    } catch (e) {
      console.error('опрос:', e); // исключение не останавливает опрос навсегда (хвост Х19)
    } finally {
      busy = false;
    }
    if (!stopped && !document.hidden) timer = setTimeout(tick, ms);
  };
  const onVisible = () => {
    if (!document.hidden && !timer && !busy && !stopped) tick();
  };
  document.addEventListener('visibilitychange', onVisible);
  tick();
  return {
    stop() {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisible);
    },
    now() {
      if (busy || stopped) return;
      clearTimeout(timer);
      tick();
    },
  };
}

// openPlayer — «Смотреть» на этом ПК: ссылка kinodom:// открывает плеер. Если обработчик ссылки не
// установлен (kinodom protocol install / инсталлятор), браузер молча ничего не делает — тогда через
// 1,5 с, если плеер не забрал фокус, открывается запасной адрес (.m3u8). env — для тестов.
export function openPlayer(launchUrl, fallbackUrl, env = { win: window, loc: location, doc: document, wait: (f) => setTimeout(f, 1500) }) {
  let left = false;
  const onBlur = () => {
    left = true;
  };
  env.win.addEventListener('blur', onBlur);
  env.loc.href = launchUrl;
  env.wait(() => {
    env.win.removeEventListener('blur', onBlur);
    if (!left && !env.doc.hidden) env.loc.href = fallbackUrl;
  });
}

// copyText — текст в буфер обмена. Пульт открывают по http с адреса в домашней сети, а там
// navigator.clipboard нет — запасной путь через выделение.
export async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const active = document.activeElement;
  const ta = h('textarea', { style: { position: 'fixed', top: '-100px', opacity: '0' }, readonly: true }, text);
  document.body.append(ta);
  ta.select();
  const ok = document.execCommand('copy');
  ta.remove();
  // Фокус — обратно на кнопку: иначе на пульте ТВ следующая стрелка начинает с первого элемента (Х25).
  if (active && active.focus) active.focus({ preventScroll: true });
  if (!ok) throw new Error('не удалось скопировать');
}

// openModal — окно поверх экрана: остальное недоступно (inert — стрелки пульта не уходят за окно),
// «Назад» пульта и Escape вызывают onCancel, а не уходят с экрана. close() убирает окно и возвращает
// фокус туда, где он был.
export function openModal(box, onCancel) {
  const before = document.activeElement;
  const others = [...document.body.children];
  for (const el of others) el.inert = true;
  const back = h('div', { class: 'dlg-back', onkeydown: (e) => {
    const typing = e.target.tagName === 'INPUT';
    if (e.key === 'Escape' || e.key === 'GoBack' || e.key === 'BrowserBack' || (e.key === 'Backspace' && !typing)) {
      e.preventDefault();
      e.stopPropagation();
      onCancel();
    }
  } }, box);
  document.body.append(back);
  return {
    close() {
      back.remove();
      for (const el of others) el.inert = false;
      if (before && before.focus) before.focus({ preventScroll: true });
    },
  };
}

// confirmDialog — «Да» / «Нет» (спека 11b, 4.1): Promise<boolean>; фокус сразу на «Да».
export function confirmDialog({ title, yes = 'Да', no = 'Нет' }) {
  return new Promise((resolve) => {
    let modal = null;
    const done = (v) => {
      modal.close();
      resolve(v);
    };
    const yesBtn = h('button', { class: 'btn inv', type: 'button', 'data-key': 'dlg-yes', onclick: () => done(true) }, yes);
    const noBtn = h('button', { class: 'btn', type: 'button', 'data-key': 'dlg-no', onclick: () => done(false) }, no);
    const box = h('div', { class: 'dlg card', role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      h('div', { class: 'h' }, title), h('div', { class: 'row gap10' }, yesBtn, noBtn));
    modal = openModal(box, () => done(false));
    yesBtn.focus({ preventScroll: true });
  });
}

// store — запомнить мелочь в браузере (последняя вкладка, раздел); без localStorage — не помнить.
export const store = {
  get(key) {
    try {
      return localStorage.getItem('kinodom.' + key);
    } catch {
      return null;
    }
  },
  set(key, value) {
    try {
      localStorage.setItem('kinodom.' + key, value);
    } catch {
      // приватное окно или запрет — просто не запоминаем
    }
  },
};
