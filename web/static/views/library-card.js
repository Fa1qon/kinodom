// Карточка медиатеки (спека этапа 9, раздел 6.2): постер, описание, рейтинги; «Смотреть» /
// «Продолжить»; у сериала — сезоны и серии с отметками этого устройства; версии («Есть дубли»);
// правки из домашней сети — «Это другой фильм», «Разметить вручную», «Перенести в категорию».
import { h, fill, icon, keepFocus, rating, size, openPlayer, poll, ready, confirmDialog } from '../ui.js';
import { get, put, del } from '../api.js';
import { whereStopped, resumeIndex } from './history.js';
import { libPoster, continueLabel } from './library.js';
import { skippedText } from './downloads.js';

// deleteRequest — «Удалить» версии карточки (план 14В): скачанное — как в «Загрузках» (вся раздача), своё —
// файл или папка с диска через медиатеку. {path, text} — запрос и вопрос подтверждения.
export function deleteRequest(v) {
  const name = (v.path || '').split(/[\\/]/).filter(Boolean).pop() || '';
  if (v.hash && !v.hash.startsWith('lib-')) return { path: `/downloads/${v.hash}`, text: `Удалить скачанное «${name}» с диска?` };
  return { path: `/library/units/${v.unit}`, text: `Удалить «${name}» с диска навсегда?`, safe: true };
}

// afterDelete — куда после «Удалить» версии unit карточки card (ответ res): серии, которые смотрят, остались —
// на месте с подписью; у карточки есть другие версии — на месте; последняя — в «Медиатеку» (ревью 14В).
export function afterDelete(card, unit, res) {
  const note = skippedText(res);
  if (note) return { go: null, note };
  const left = (card.versions || []).filter((v) => v.unit !== unit).length;
  return { go: left ? null : '#/library', note: '' };
}

// seasonsOf — серии по сезонам и разделам: без сезона — первыми (вступление курса), потом сезоны по
// номеру, потом разделы (главы) в порядке сервера.
export function seasonsOf(episodes) {
  const groups = new Map();
  for (const e of episodes) {
    const key = e.season > 0 ? `s${e.season}` : e.section ? `x${e.section}` : '';
    if (!groups.has(key)) groups.set(key, { key, season: e.season, section: e.section, title: e.season > 0 ? `Сезон ${e.season}` : e.section || '', items: [] });
    groups.get(key).items.push(e);
  }
  const rank = (g) => (g.key === '' ? 0 : g.season > 0 ? 1 : 2);
  return [...groups.values()].sort((a, b) => rank(a) - rank(b) || (a.season - b.season));
}

// episodeLabel — «1×02», «7» или имя файла без расширения.
export function episodeLabel(e) {
  if (e.episode > 0) return e.season > 0 ? `${e.season}×${String(e.episode).padStart(2, '0')}` : String(e.episode);
  const dot = e.name.lastIndexOf('.');
  return dot > 0 ? e.name.slice(0, dot) : e.name;
}

// defaultVersion — версия по умолчанию: которую это устройство смотрело последней, иначе скачанная,
// иначе первая.
export function defaultVersion(c) {
  if (c.lastVersion && c.versions.some((v) => v.unit === c.lastVersion)) return c.lastVersion;
  const dl = c.versions.find((v) => v.source === 'Скачано');
  return (dl || c.versions[0]).unit;
}

// libraryPlayerLink — плеер на другом устройстве. Android — VLC ссылкой intent: у сериала — .m3u8
// (следующие серии подряд, место — в .m3u8), у фильма — поток с местом (l.position, мс); остальные
// устройства — .m3u8.
export function libraryPlayerLink(res, hasNext, ua = navigator.userAgent) {
  if (!/Android/i.test(ua)) return res.m3uUrl;
  const tail = `S.title=${encodeURIComponent(res.title)};S.browser_fallback_url=${encodeURIComponent(res.m3uUrl)};end`;
  if (hasNext) {
    const u = new URL(res.m3uUrl);
    return `intent://${u.host}${u.pathname}${u.search}#Intent;scheme=${u.protocol.replace(':', '')};type=audio/x-mpegurl;package=org.videolan.vlc;${tail}`;
  }
  const u = new URL(res.streamUrl);
  return `intent://${u.host}${u.pathname}#Intent;scheme=${u.protocol.replace(':', '')};type=video/*;package=org.videolan.vlc;`
    + (res.startSec > 0 ? `l.position=${res.startSec * 1000};` : '') + tail;
}

// playFile — «Смотреть» файл медиатеки: на этом ПК — плеер ссылкой kinodom://, иначе — VLC или .m3u8.
export async function playFile(file, ctx, fromStart = false, hasNext = true) {
  const res = await get(`/library/files/${file}/play` + (fromStart ? '?fromStart=1' : ''));
  if (ctx.local && res.launchUrl) openPlayer(res.launchUrl, res.m3uUrl, ctx.status && ctx.status.protocol);
  else location.href = libraryPlayerLink(res, hasNext);
  return res;
}

const kpOf = (link) => {
  const m = /(?:film|series)\/(\d+)/.exec(link) || /^\s*(\d+)\s*$/.exec(link);
  return m ? m[1] : '';
};

export function render(root, r, ctx) {
  const key = r.parts[1];
  let alive = true;
  let c = null;
  let error = ''; // карточка не загрузилась
  let failed = ''; // последнее действие не удалось — видно до следующего действия
  let unit = 0; // выбранная версия
  let group = null; // выбранный сезон или раздел; null — где продолжать
  let descOpen = false;
  let cats = null;
  let editing = ''; // открытая правка: link, manual, move
  const link = h('input', { class: 'input', name: 'kinopoisk', placeholder: 'Ссылка на Кинопоиск', 'aria-label': 'Ссылка на Кинопоиск', 'data-key': 'kp-link' });
  const title = h('input', { class: 'input', name: 'title', placeholder: 'Название', 'aria-label': 'Название', 'data-key': 'manual-title' });
  const year = h('input', { class: 'input year', name: 'year', placeholder: 'Год', 'aria-label': 'Год', inputmode: 'numeric', 'data-key': 'manual-year' });
  const move = h('select', { class: 'input', name: 'category', 'aria-label': 'Категория', 'data-key': 'move-cat' });

  const back = h('a', { class: 'back', href: '#/library' }, icon('chevron_left', 18), 'Медиатека');
  const cover = h('div', { class: 'rel-cover' });
  const info = h('div', { class: 'rel-info' });
  const eps = h('div', { class: 'rel-live' }); // на узком экране — под панелью, как у раздачи
  const side = h('aside', { class: 'panel', 'aria-label': 'Версии и правка' });
  root.append(h('div', { class: 'screen release libcard' }, back, h('div', { class: 'rel-grid' }, cover, h('div', { class: 'rel-main' }, info, eps), side)));

  const cardPoll = poll(async () => {
    try {
      c = await get(`/library/cards/${encodeURIComponent(key)}`);
      error = '';
    } catch (e) {
      if (e.status === 404) {
        cardPoll.stop();
        fill(info, h('p', { class: 'error' }, 'Такой карточки в медиатеке нет'));
        return;
      }
      error = e.message;
    }
    if (!alive || !c) return;
    if (!c.versions.some((v) => v.unit === unit)) unit = defaultVersion(c);
    draw();
  }, 5000);

  async function act(fn) {
    try {
      await fn();
      failed = '';
    } catch (e) {
      failed = e.message;
    }
    if (alive) {
      draw();
      cardPoll.now();
    }
  }

  const version = () => c.versions.find((v) => v.unit === unit) || c.versions[0];

  function draw() {
    const v = version();
    keepFocus(root, () => {
      cover.replaceChildren(libPoster(c, 'poster big'));
      const desc = c.description ? h('p', { class: descOpen ? 'desc' : 'desc clamp' }, c.description) : null;
      fill(info, h('h1', null, c.title),
        h('div', { class: 'muted sub' }, [c.nameOrig, c.year || null, c.genres.join(', ') || null].filter(Boolean).join(' · ')),
        h('div', { class: 'tags' },
          c.rating > 0 ? h('span', { class: 'tag strong' }, 'КП ' + rating(c.rating)) : null,
          c.ratingImdb > 0 ? h('span', { class: 'tag' }, 'IMDb ' + rating(c.ratingImdb)) : null,
          c.dupes ? h('span', { class: 'tag' }, 'Есть дубли') : null,
          c.deleteInDays !== null && c.deleteInDays !== undefined ? h('span', { class: 'tag' }, `удалится через ${c.deleteInDays} дн.`) : null),
        desc, watchBlock(v), error || failed ? h('p', { class: 'error' }, failed || error) : null);
      if (desc && !descOpen) {
        requestAnimationFrame(() => {
          if (desc.isConnected && desc.scrollHeight > desc.clientHeight + 2) {
            desc.after(h('button', { class: 'link', type: 'button', onclick: () => {
              descOpen = true;
              draw();
            } }, 'Показать полностью'));
          }
        });
      }
      fill(eps, v.episodes.length > 1 ? episodes(v) : null);
      fill(side, ...versions(), ...edits(v));
    });
  }

  // resumeEp — серия, которую продолжать на этом устройстве (правило 8c), иначе первая.
  function resumeEp(v) {
    const progress = v.episodes.map((e) => e.progress).filter(Boolean);
    const i = resumeIndex(v.episodes.map((e) => e.index), progress);
    return { ep: v.episodes.find((e) => e.index === i) || v.episodes[0], resumed: i !== null };
  }

  function watchBlock(v) {
    if (!v.episodes.length) return h('p', { class: 'muted' }, 'Файлов нет');
    const { ep, resumed } = resumeEp(v);
    const p = ep.progress;
    const at = p && !p.watched ? p.positionSec : 0;
    const film = v.episodes.length === 1;
    const label = film ? (at > 0 ? `Продолжить ${continueLabel({ positionSec: at })}` : 'Смотреть')
      : resumed ? `Продолжить: ${continueLabel({ season: ep.season, episode: ep.episode, positionSec: at })}` : `Смотреть: ${episodeLabel(ep)}`;
    return h('div', { class: 'row wrap gap10' },
      h('button', { class: 'btn inv big', type: 'button', 'data-key': 'watch', onclick: () => act(() => playFile(ep.file, ctx, false, !film)) }, icon('play_arrow'), label),
      at > 0 ? h('button', { class: 'btn big', type: 'button', 'data-key': 'from-start', onclick: () => act(() => playFile(ep.file, ctx, true, !film)) },
        icon('history'), 'С начала') : null);
  }

  function episodes(v) {
    const groups = seasonsOf(v.episodes);
    const { ep } = resumeEp(v);
    const cur = groups.find((g) => g.key === group) || groups.find((g) => g.items.includes(ep)) || groups[0];
    const tabs = groups.length > 1 ? h('div', { class: 'filters', role: 'tablist', 'aria-label': 'Сезоны' }, groups.map((g) => h('a', {
      class: g === cur ? 'fil on' : 'fil', href: '#', role: 'tab', 'aria-selected': String(g === cur), 'data-key': `season-${g.key || 'none'}`,
      onclick: (e) => {
        e.preventDefault();
        group = g.key;
        draw();
      } }, g.title || 'Другое'))) : null;
    const downloaded = v.source === 'Скачано';
    return h('section', { class: 'episodes', 'aria-label': 'Серии' }, h('h2', null, 'Серии'), tabs,
      cur.items.map((e) => {
        const p = e.progress;
        const rd = downloaded ? ready[e.readiness || 'none'] || ready.none : ready.done;
        const seen = !!(p && p.watched);
        return h('div', { class: 'ep' },
          h('span', { class: 'num' }, episodeLabel(e).length <= 5 ? episodeLabel(e) : ''),
          h('span', { class: 'ep-main' },
            h('span', { class: 'ep-title' }, seen ? icon('check', 18, 'просмотрено') : null, h('span', { class: 'ep-name', title: e.name }, episodeLabel(e).length > 5 ? episodeLabel(e) : e.name)),
            h('span', { class: 'muted small' }, [size(e.size), whereStopped(p)].filter(Boolean).join(' · ')),
            p && !p.watched && p.fraction > 0 ? h('div', { class: 'track pos-track' }, h('div', { style: { width: `${Math.round(p.fraction * 100)}%`, background: 'var(--text)' } })) : null),
          h('button', { class: 'btn', type: 'button', style: downloaded && rd.color ? { borderColor: rd.color, color: rd.color } : null,
            'data-key': `watch-${e.file}`, 'aria-label': `Смотреть ${episodeLabel(e)}`, onclick: () => act(() => playFile(e.file, ctx, false, true)) }, icon('play_arrow'), 'Смотреть'),
          ctx.canEdit ? h('button', { class: 'sq', type: 'button', 'data-key': `seen-${e.file}`, 'aria-pressed': String(seen),
            'aria-label': seen ? 'Не просмотрено' : 'Отметить просмотренной', onclick: () => act(() => put(`/history/${e.hash}/${e.index}`, { watched: !seen })) },
          icon(seen ? 'visibility_off' : 'check')) : null);
      }));
  }

  function versions() {
    if (c.versions.length < 2) {
      const v = c.versions[0];
      return v ? [h('div', { class: 'muted small' }, [v.source, v.quality, v.format, size(v.size)].filter(Boolean).join(' · ')), h('div', { class: 'muted small path' }, v.path)] : [];
    }
    return [h('div', { class: 'h' }, 'Есть дубли'), h('div', { class: 'versions' }, c.versions.map((v) => h('button', {
      class: v.unit === unit ? 'ver on' : 'ver', type: 'button', 'aria-pressed': String(v.unit === unit), 'data-key': `ver-${v.unit}`,
      onclick: () => {
        unit = v.unit;
        group = null;
        draw();
      } }, h('span', { class: 'strong' }, [v.source, v.quality, v.format, size(v.size)].filter(Boolean).join(' · ')), h('span', { class: 'muted small path' }, v.path))))];
  }

  function edits(v) {
    if (!ctx.canEdit) return [];
    const toggle = (id, text, ic) => h('button', { class: editing === id ? 'btn on' : 'btn', type: 'button', 'data-key': `edit-${id}`, 'aria-expanded': String(editing === id),
      onclick: () => {
        editing = editing === id ? '' : id;
        if (id === 'manual' && !title.value) title.value = c.title;
        if (id === 'move' && !cats) {
          get('/library/categories').then((cs) => {
            cats = cs;
            if (alive) draw();
          }, () => {});
        }
        draw();
      } }, icon(ic), text);
    const out = [h('div', { class: 'h' }, 'Правка'), toggle('link', 'Это другой фильм', 'swap_horiz')];
    if (editing === 'link') {
      out.push(h('form', { class: 'row gap10', onsubmit: (e) => {
        e.preventDefault();
        const n = kpOf(link.value);
        act(async () => {
          await put(`/library/units/${v.unit}`, { kinopoisk: link.value });
          if (n && `kp-${n}` !== key) ctx.go(`#/library/kp-${n}`);
        });
      } }, link, h('button', { class: 'btn', type: 'submit', 'data-key': 'kp-save' }, 'Привязать')));
    }
    out.push(toggle('manual', 'Разметить вручную', 'save'));
    if (editing === 'manual') {
      out.push(h('form', { class: 'row gap10', onsubmit: (e) => {
        e.preventDefault();
        act(async () => {
          await put(`/library/units/${v.unit}`, { manual: { title: title.value, year: parseInt(year.value, 10) || 0 } });
          if (`u-${v.unit}` !== key) ctx.go(`#/library/u-${v.unit}`);
        });
      } }, title, year, h('button', { class: 'btn', type: 'submit', 'data-key': 'manual-save' }, 'Сохранить')));
    }
    out.push(h('button', { class: 'btn', type: 'button', 'data-key': 'delete', onclick: async () => {
      const req = deleteRequest(v);
      if (!(await confirmDialog({ title: req.text, yes: 'Удалить', safe: !!req.safe }))) return;
      act(async () => {
        const step = afterDelete(c, v.unit, await del(req.path));
        if (step.go) ctx.go(step.go);
        else if (step.note) throw new Error(step.note);
      });
    } }, icon('delete'), 'Удалить'));
    out.push(toggle('move', 'Перенести в категорию', 'folder'));
    if (editing === 'move' && cats) {
      if (!move.options.length) for (const x of cats) move.append(h('option', { value: String(x.id), selected: x.id === c.category }, x.name));
      out.push(h('div', { class: 'row gap10' }, move,
        h('button', { class: 'btn', type: 'button', 'data-key': 'move-save', onclick: () => act(() => put(`/library/cards/${encodeURIComponent(key)}`, { category: Number(move.value) })) }, 'Перенести'),
        h('button', { class: 'btn', type: 'button', 'data-key': 'move-reset', onclick: () => act(() => put(`/library/cards/${encodeURIComponent(key)}`, { category: null })) }, 'Как было')));
    }
    return out;
  }

  return () => {
    alive = false;
    cardPoll.stop();
  };
}
