// «История» (спека этапа 8, раздел 7.5): раздачи, которые смотрели на этом устройстве, по времени
// последнего просмотра; последняя серия и где остановились; строка ведёт на экран раздачи; крестик
// убирает раздачу из истории (из домашней сети).
import { h, fill, icon, poll, keepFocus, day, baseName } from '../ui.js';
import { get, del } from '../api.js';

// duration — «52 мин», «1 ч 58 мин».
export function duration(sec) {
  const m = Math.round(sec / 60);
  if (m < 60) return `${m} мин`;
  return `${Math.floor(m / 60)} ч${m % 60 ? ` ${m % 60} мин` : ''}`;
}

// whereStopped — «остановились на 52 мин из 1 ч 58 мин», «на 43 %», «досмотрено»; "" — не начинали.
export function whereStopped(p) {
  if (!p) return '';
  if (p.watched) return 'досмотрено';
  if (p.positionSec > 0 && p.durationSec > 0) return `остановились на ${duration(p.positionSec)} из ${duration(p.durationSec)}`;
  if (p.fraction > 0) return `остановились на ${Math.round(p.fraction * 100)} %`;
  return '';
}

// resumeIndex — файл, который продолжать (спека этапа 8, раздел 7.5): начатый и недосмотренный,
// смотренный последним; иначе следующий по порядку после последнего просмотренного; null — истории нет.
// files — номера видеофайлов в порядке экрана, progress — места из /history/{hash}.
export function resumeIndex(files, progress) {
  if (!progress.length) return null;
  const byIndex = new Map(progress.map((p) => [p.index, p]));
  const recent = [...progress].sort((a, b) => new Date(b.updatedAt) - new Date(a.updatedAt));
  const started = recent.find((p) => !p.watched && p.fraction > 0 && files.includes(p.index));
  if (started) return started.index;
  const lastWatched = recent.find((p) => p.watched && files.includes(p.index));
  if (!lastWatched) return null;
  const from = files.indexOf(lastWatched.index);
  for (let i = from + 1; i < files.length; i++) {
    const p = byIndex.get(files[i]);
    if (!p || !p.watched) return files[i];
  }
  return null;
}

export function render(root, r, ctx) {
  let alive = true;
  let data = null;
  let error = '';
  let confirm = '';
  const list = h('div', { class: 'dl-list' });
  root.append(h('div', { class: 'screen' }, h('h1', null, 'История'), list));

  const listPoll = poll(async () => {
    try {
      data = await get('/history');
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }, 30000);

  async function remove(hash) {
    try {
      await del(`/history/${hash}`);
    } catch (e) {
      error = e.message;
    }
    confirm = '';
    listPoll.now();
  }

  function draw() {
    if (!data) {
      fill(list, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => fill(list, error ? h('p', { class: 'error' }, error) : null,
      data.items.length ? data.items.map(row) : h('p', { class: 'empty' }, 'Здесь появится то, что смотрели на этом устройстве')));
  }

  function row(it) {
    const rel = it.release;
    const title = rel ? rel.title : it.hash.slice(0, 8);
    const thumb = h('div', { class: 'thumb' });
    if (rel && rel.imageKey) thumb.append(h('img', { src: `/img/${rel.imageKey}`, alt: '', loading: 'lazy' }));
    const file = it.file ? baseName(it.file) : `файл ${it.last.index + 1}`;
    const lines = [
      [it.count > 1 ? file : null, whereStopped(it.last)].filter(Boolean).join(' · '),
      [it.count > 1 ? `просмотрено ${it.watched} из ${it.count}` : null, day(it.updatedAt)].filter(Boolean).join(' · '),
    ];
    const main = h('span', { class: 'dl-name' }, h('span', { class: 'strong ellipsis', title }, title),
      h('span', { class: 'muted small ellipsis' }, lines[0]), h('span', { class: 'muted small' }, lines[1]));
    const conf = confirm === it.hash;
    return h('div', { class: 'dl' },
      rel ? h('a', { class: 'hist-link', href: `#/release/${rel.id}`, 'data-key': `open-${it.hash}` }, thumb, main) : h('div', { class: 'hist-link' }, thumb, main),
      ctx.canEdit ? h('button', { class: conf ? 'btn danger' : 'sq', type: 'button', 'data-key': `forget-${it.hash}`,
        'aria-label': conf ? 'Убрать?' : 'Убрать из истории', onclick: () => {
          if (!conf) {
            confirm = it.hash;
            draw();
            setTimeout(() => {
              if (confirm === it.hash) {
                confirm = '';
                if (alive) draw();
              }
            }, 3000);
            return;
          }
          remove(it.hash);
        } }, icon('close'), conf ? 'Убрать?' : null) : null);
  }

  return () => {
    alive = false;
    listPoll.stop();
  };
}

