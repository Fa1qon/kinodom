// «Что посмотреть»: подборка по вкусу человека (журнал просмотров Kinodom, оценки, отметки) и по
// настроению; «смотрим вместе» — на нескольких профилях семьи. Карточка ведёт в поиск раздач фильма.
import { h, fill, icon, rating, keepFocus, store } from '../ui.js';
import { get, post, put } from '../api.js';
import { filmPoster, filmSearchHref } from './kpcat.js';

const KIND = 'recs.kind';

// recLine — год, длительность, оценки.
export function recLine(f) {
  return [f.year || null, f.kind === 'series' ? 'сериал' : f.duration ? `${f.duration} мин` : null,
    f.kinopoisk > 0 ? 'КП ' + rating(f.kinopoisk) : null].filter(Boolean).join(' · ');
}

// recsQuery — адрес подборки: просьба словами, настроения, вид, с кем вместе, «удиви».
export function recsQuery({ text = '', moods = [], kind = '', with: together = [], surprise = false, limit = 18 }) {
  const q = new URLSearchParams();
  if (text.trim()) q.set('q', text.trim());
  for (const m of moods) q.append('mood', m);
  if (kind) q.set('kind', kind);
  if (together.length) q.set('with', together.join(','));
  if (surprise) q.set('surprise', '1');
  q.set('limit', String(limit));
  return '/recs?' + q;
}

// RATE — быстрые оценки: на ТВ десять кнопок долго листать, четырёх хватает, чтобы понять вкус.
export const RATE = [['Шедевр', 10], ['Хорошо', 8], ['Так себе', 6], ['Плохо', 3]];

export function render(root, r, ctx) {
  let alive = true;
  let status = null;
  let text = r.query.get('q') || '';
  let moods = new Set(r.query.getAll('mood'));
  let kind = store.get(KIND) || '';
  let together = new Set();
  let surprise = false;
  let items = null;
  let note = '';
  let error = '';
  let busy = false;
  let rateFor = 0; // карточка, где открыт выбор оценки
  let done = new Map(); // kp → что отметили («Хочу», «Оценка 8», «Не моё»)
  let quiz = null;
  const quizSkip = []; // «Не смотрел» в быстрой оценке — не показывать снова за этот заход
  let taste = null;
  let wanted = null; // «Хочу посмотреть»: null — не загружено
  let wantRate = 0; // карточка списка, где открыт выбор оценки
  let tab = 'recs';
  // Импорт с Кинопоиска — в состоянии экрана: экран перерисовывается сам (постеры подборки), и поле со
  // ссылкой и итог не должны пропадать посреди десятисекундного импорта.
  let kpProfile = '';
  let kpBusy = false;
  let kpMsg = '';
  let kpError = false;

  const body = h('div', { class: 'recs-body' });
  root.append(h('div', { class: 'screen recs' }, h('h1', null, 'Что посмотреть'), body));

  async function loadStatus() {
    try {
      status = await get('/recs/status');
    } catch (e) {
      error = e.message;
    }
    if (!alive) return;
    if (status && status.installed) await pick();
    else draw();
  }

  async function pick() {
    busy = true;
    draw();
    try {
      const v = await get(recsQuery({ text, moods: [...moods], kind, with: [...together], surprise }));
      items = v.items;
      note = v.note || '';
      moods = new Set(v.query.moods || []); // настроения, понятые из просьбы, — тоже подсвечены
      refreshCards();
      error = '';
      done = new Map();
    } catch (e) {
      error = e.message;
    }
    busy = false;
    if (alive) draw();
  }

  // refreshCards — фильмы без постера Kinodom дозапрашивает у Кинопоиска после показа (по одному в 2–3 с): через
  // 8, 25, 50 и 90 с их карточки подставляются на месте, порядок подборки не меняется.
  let cardTimers = [];
  function refreshCards() {
    cardTimers.forEach(clearTimeout);
    cardTimers = [8000, 25000, 50000, 90000].map((ms) => setTimeout(async () => {
      const ids = (items || []).filter((it) => !it.film.poster).map((it) => it.film.id);
      if (!alive || !ids.length) return;
      try {
        const v = await get('/recs/films?ids=' + ids.join(','));
        const byId = new Map(v.items.map((f) => [f.id, f]));
        items = items.map((it) => (byId.has(it.film.id) ? { ...it, film: byId.get(it.film.id) } : it));
        if (alive) draw();
      } catch { /* не страшно: постер появится при следующей подборке */ }
    }, ms));
  }

  async function act(f, what, score) {
    try {
      if (what === 'rate') await post('/recs/rate', { kpId: f.id, score });
      else await post('/recs/mark', { kpId: f.id, mark: what });
      done.set(f.id, what === 'rate' ? `Оценка ${score}` : what === 'want' ? 'В «Хочу посмотреть»' : what === 'seen' ? 'Видел' : 'Не моё — учту');
      wanted = null; // список «Хочу посмотреть» перечитается
      rateFor = 0;
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }

  function draw() {
    if (!status) {
      fill(body, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    if (!status.installed) {
      fill(body, h('div', { class: 'recs-empty' },
        h('p', null, 'Рекомендации ещё не установлены.'),
        h('p', { class: 'muted' }, 'Пакет модели recs-model.db входит в установщик Kinodom — переустановите программу. ' +
          'Свежий пакет можно поставить и вручную: положить в папку данных Kinodom и перезапустить службу.')));
      return;
    }
    keepFocus(root, () => fill(body,
      tabs(),
      tab === 'recs' ? recsTab() : tab === 'want' ? wantTab() : tab === 'taste' ? tasteTab() : setupTab()));
  }

  function tabs() {
    const t = (id, title) => h('a', { class: tab === id ? 'on' : '', href: 'javascript:void 0', 'data-key': `recs-tab-${id}`,
      onclick: () => { tab = id; if (id === 'taste') loadTaste(); if (id === 'want') loadWanted(); if (id === 'setup') { loadQuiz(); reloadStatus(); } draw(); } }, title);
    const me = (status.people || []).find((p) => p.owner === status.owner);
    return h('div', { class: 'row recs-head' },
      h('div', { class: 'tabs' }, t('recs', 'Подборка'), t('want', wanted && wanted.length ? `Хочу посмотреть · ${wanted.length}` : 'Хочу посмотреть'),
        t('taste', 'Мой вкус'), t('setup', 'Оценки')),
      h('div', { class: 'grow' }),
      h('span', { class: 'muted small' }, me ? `Профиль: ${me.name}` : 'Профиль не выбран — вкус этого устройства'));
  }

  function recsTab() {
    const others = (status.people || []).filter((p) => p.owner !== status.owner);
    const input = h('input', { class: 'recs-ask', type: 'text', value: text, placeholder: 'Чего хочется? «уютное на вечер», «сериал как „Тьма“», «смешное с детьми»',
      'data-key': 'recs-ask', oninput: (e) => { text = e.target.value; }, onkeydown: (e) => { if (e.key === 'Enter') pick(); } });
    const chip = (on, title, key, fn) => h('button', { class: on ? 'btn chip on' : 'btn chip', type: 'button', 'aria-pressed': String(on), 'data-key': key, onclick: fn }, title);
    return h('div', null,
      h('div', { class: 'row recs-askrow' }, input,
        h('button', { class: 'btn inv', type: 'button', 'data-key': 'recs-go', disabled: busy, onclick: pick }, icon('star'), 'Подобрать')),
      h('div', { class: 'recs-chips', 'aria-label': 'Настроение' }, status.moods.map((m) => chip(moods.has(m.Key), m.Name, `mood-${m.Key}`, () => {
        moods.has(m.Key) ? moods.delete(m.Key) : moods.add(m.Key);
        pick();
      }))),
      h('div', { class: 'row recs-chips' },
        chip(kind === '', 'Всё', 'kind-all', () => { kind = ''; store.set(KIND, kind); pick(); }),
        chip(kind === 'film', 'Фильмы', 'kind-film', () => { kind = 'film'; store.set(KIND, kind); pick(); }),
        chip(kind === 'series', 'Сериалы', 'kind-series', () => { kind = 'series'; store.set(KIND, kind); pick(); }),
        chip(surprise, 'Удиви', 'surprise', () => { surprise = !surprise; pick(); }),
        others.length ? h('span', { class: 'muted small recs-with' }, 'Смотрим вместе с:') : null,
        others.map((p) => chip(together.has(p.owner), p.name, `with-${p.owner}`, () => {
          together.has(p.owner) ? together.delete(p.owner) : together.add(p.owner);
          pick();
        }))),
      note ? h('p', { class: 'muted small' }, note) : null,
      error ? h('p', { class: 'error' }, error) : null,
      busy && !items ? h('p', { class: 'muted' }, 'Подбираю…') : null,
      items && !items.length ? h('p', { class: 'empty' }, 'Под такую просьбу ничего не нашлось — попробуйте мягче') : null,
      items ? h('div', { class: 'grid recs-grid' + (busy ? ' busy' : '') }, items.map(card)) : null);
  }

  function card(it) {
    const f = it.film;
    const mark = done.get(f.id);
    const actions = rateFor === f.id
      ? h('div', { class: 'recs-actions' }, RATE.map(([title, v]) => h('button', { class: 'btn', type: 'button', 'data-key': `rate-${f.id}-${v}`, onclick: () => act(f, 'rate', v) }, title)),
        h('button', { class: 'btn', type: 'button', 'data-key': `seen-${f.id}`, onclick: () => act(f, 'seen') }, 'Без оценки'))
      : mark ? h('div', { class: 'recs-actions muted small' }, mark)
        : h('div', { class: 'recs-actions' },
          h('a', { class: 'btn inv', href: filmSearchHref(f), 'data-key': `find-${f.id}` }, icon('search'), 'Найти'),
          h('button', { class: 'btn sq', type: 'button', title: 'Хочу посмотреть', 'aria-label': 'Хочу посмотреть', 'data-key': `want-${f.id}`, onclick: () => act(f, 'want') }, icon('add')),
          h('button', { class: 'btn sq', type: 'button', title: 'Уже видел', 'aria-label': 'Уже видел', 'data-key': `seen-${f.id}`, onclick: () => { rateFor = f.id; draw(); } }, icon('visibility')),
          h('button', { class: 'btn sq', type: 'button', title: 'Не моё', 'aria-label': 'Не моё', 'data-key': `no-${f.id}`, onclick: () => act(f, 'notmine') }, icon('close')));
    return h('div', { class: 'entry rec-card', 'data-key': `rec-${f.id}` },
      filmPoster(f),
      h('div', { class: 'etitle' }, f.title),
      h('div', { class: 'muted small' }, recLine(f)),
      h('div', { class: 'small rec-reason' }, it.reason),
      actions);
  }

  // reloadStatus — ключ Кинопоиска могли задать в настройках после открытия страницы.
  async function reloadStatus() {
    try { status = await get('/recs/status'); } catch { return; }
    if (alive) draw();
  }

  async function importKP() {
    if (kpBusy) return;
    kpBusy = true;
    kpMsg = 'Забираю оценки с Кинопоиска — это до полуминуты…';
    kpError = false;
    draw();
    try {
      const v = await post('/recs/import-kp', { profile: kpProfile });
      kpMsg = v.imported ? `Готово: ${v.imported} оценок. Подборка обновлена.` : 'Оценок не нашлось — проверьте, что они открыты в профиле.';
      items = null; // подборка пересчитается с новыми оценками
      taste = null;
      loadQuiz();
      if (v.imported) pick();
    } catch (e) {
      kpMsg = e.message;
      kpError = true;
    }
    kpBusy = false;
    if (alive) draw();
  }

  // «Хочу посмотреть»: отмеченное «+» в подборке и «Смотреть позже» Kinodom.
  async function loadWanted() {
    try { wanted = (await get('/recs/wanted')).items; } catch (e) { error = e.message; }
    if (alive) draw();
  }

  async function wantAct(f, what, score) {
    try {
      if (what === 'rate') await post('/recs/rate', { kpId: f.id, score });
      else await post('/recs/mark', { kpId: f.id, mark: what });
    } catch (e) { error = e.message; }
    wantRate = 0;
    loadWanted();
  }

  function wantTab() {
    if (!wanted) return h('p', { class: 'muted' }, 'Загружается…');
    if (!wanted.length) return h('p', { class: 'empty' }, 'Список пуст. Нажимайте «+» на карточках подборки — фильмы появятся здесь. «Смотреть позже» Kinodom тоже здесь.');
    return h('div', { class: 'grid recs-grid' }, wanted.map((f) => h('div', { class: 'entry rec-card', 'data-key': `want-card-${f.id}` },
      filmPoster(f),
      h('div', { class: 'etitle' }, f.title),
      h('div', { class: 'muted small' }, recLine(f)),
      wantRate === f.id
        ? h('div', { class: 'recs-actions' }, RATE.map(([title, v]) => h('button', { class: 'btn', type: 'button', 'data-key': `wrate-${f.id}-${v}`, onclick: () => wantAct(f, 'rate', v) }, title)),
          h('button', { class: 'btn', type: 'button', 'data-key': `wseen-${f.id}`, onclick: () => wantAct(f, 'seen') }, 'Без оценки'))
        : h('div', { class: 'recs-actions' },
          h('a', { class: 'btn inv', href: filmSearchHref(f), 'data-key': `wfind-${f.id}` }, icon('search'), 'Найти'),
          h('button', { class: 'btn sq', type: 'button', title: 'Посмотрел', 'aria-label': 'Посмотрел', 'data-key': `wwatched-${f.id}`, onclick: () => { wantRate = f.id; draw(); } }, icon('visibility')),
          h('button', { class: 'btn sq', type: 'button', title: 'Убрать из списка', 'aria-label': 'Убрать из списка', 'data-key': `wdrop-${f.id}`, onclick: () => wantAct(f, '') }, icon('close'))))));
  }

  async function loadTaste() {
    try { taste = await get('/recs/taste'); } catch (e) { error = e.message; }
    if (alive) draw();
  }

  function tasteTab() {
    if (!taste) return h('p', { class: 'muted' }, 'Загружается…');
    const row = (it) => h('div', { class: 'recs-taste-item' }, h('span', { class: 'strong' }, it.film.title), h('span', { class: 'muted small' }, it.why));
    return h('div', { class: 'recs-taste' },
      h('p', { class: 'muted' }, `Оценок: ${taste.ratings}. Фильмов в журнале просмотров: ${taste.watched}. ` +
        'Досмотренное и пересмотренное — плюс; брошенное через три дня и удалённое, не досмотрев, — минус. Только что начатое и пробу озвучки Kinodom не считает.'),
      h('div', { class: 'recs-taste-cols' },
        h('div', null, h('h2', null, 'Нравится'), taste.liked.length ? taste.liked.map(row) : h('p', { class: 'muted' }, 'Пока ничего')),
        h('div', null, h('h2', null, 'Не зашло'), taste.disliked.length ? taste.disliked.map(row) : h('p', { class: 'muted' }, 'Пока ничего'))));
  }

  async function loadQuiz() {
    try { quiz = await get('/recs/quiz?skip=' + quizSkip.join(',')); } catch (e) { error = e.message; }
    if (alive) draw();
  }

  function setupTab() {
    const kpInput = h('input', { type: 'text', placeholder: 'kinopoisk.ru/user/123456', value: kpProfile, 'data-key': 'kp-profile',
      oninput: (e) => { kpProfile = e.target.value; }, onkeydown: (e) => { if (e.key === 'Enter') importKP(); } });
    const llmKey = h('input', { type: 'password', placeholder: status.llm.configured ? 'ключ сохранён' : 'ключ polza.ai', 'data-key': 'llm-key' });
    const llmModel = h('input', { type: 'text', value: status.llm.model, 'data-key': 'llm-model' });
    const msg = h('p', { class: 'muted small' });
    const quizCards = quiz && quiz.items.length ? h('div', { class: 'grid recs-grid' }, quiz.items.map((f) => h('div', { class: 'entry rec-card', 'data-key': `quiz-${f.id}` },
      filmPoster(f), h('div', { class: 'etitle' }, f.title), h('div', { class: 'muted small' }, recLine(f)),
      h('div', { class: 'recs-actions' }, RATE.map(([title, v]) => h('button', { class: 'btn', type: 'button', 'data-key': `q-${f.id}-${v}`,
        onclick: async () => { await post('/recs/rate', { kpId: f.id, score: v }); loadQuiz(); } }, title)),
      h('button', { class: 'btn', type: 'button', 'data-key': `q-${f.id}-skip`, onclick: () => { quizSkip.push(f.id); loadQuiz(); } }, 'Не смотрел'))))) : null;
    return h('div', { class: 'recs-setup' },
      h('h2', null, 'Оценки с Кинопоиска'),
      h('p', { class: 'muted small' }, status.kpKey ? 'Откройте свои оценки в настройках приватности Кинопоиска и вставьте ссылку на профиль. Забираются последние ~1500 оценок.'
        : 'Нужен ключ Кинопоиска: Настройки → Параметры → Кинопоиск.'),
      h('div', { class: 'row' }, kpInput, h('button', { class: 'btn inv', type: 'button', 'data-key': 'kp-import', disabled: !status.kpKey || kpBusy,
        onclick: importKP }, kpBusy ? 'Забираю…' : 'Забрать')),
      kpMsg ? h('p', { class: kpError ? 'error' : 'muted small' }, kpMsg) : null,
      msg,
      h('h2', null, 'Быстрая оценка'),
      h('p', { class: 'muted small' }, `Оценено: ${quiz ? quiz.rated : '…'}. Двадцати оценок хватает, чтобы подборка стала вашей.`),
      quizCards,
      h('h2', null, 'Понимать просьбы нейросетью'),
      h('p', { class: 'muted small' }, 'Без ключа просьбы разбираются по словам. С ключом polza.ai модель лучше понимает «как „Тьма“, но легче». Фильмы она не выбирает — только понимает просьбу.'),
      h('div', { class: 'row' }, llmKey, llmModel, h('button', { class: 'btn', type: 'button', 'data-key': 'llm-save', onclick: async () => {
        const body = { llmModel: llmModel.value };
        if (llmKey.value) body.llmKey = llmKey.value;
        try { await put('/recs/config', body); status = await get('/recs/status'); draw(); } catch (e) { msg.textContent = e.message; }
      } }, 'Сохранить')));
  }

  loadStatus();
  loadWanted(); // счёт на вкладке виден сразу
  if (r.query.get('tab') === 'want') tab = 'want';
  return () => { alive = false; cardTimers.forEach(clearTimeout); };
}
