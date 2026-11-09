// «Настройки → Разделы каталога»: трекер с деревом форумов — три группы («Кино · Сериалы · Документалистика») и
// их подразделы первого уровня флажками (спека 11b, 7.1), трекер с категориями — списком (спека этапа 7, 6.3).
// Карточка — только у подключённого трекера, название — с сервера.
import { h, icon } from '../ui.js';
import { get, put } from '../api.js';
import { layout, remoteNote } from './settings-layout.js';
import { ownTrackers } from './tracker-names.js';

export function render(root, r, ctx) {
  const saveBtn = h('button', { class: 'btn inv', type: 'button', 'data-key': 'save' }, icon('save'), 'Сохранить');
  const saved = h('span', { class: 'muted small', role: 'status' });
  const content = layout(root, 'sections', 'Разделы каталога', saved, saveBtn);
  let alive = true;
  let canEdit = false;
  let groups = null; // группы t2 и их подразделы; null — не загрузились
  let t1Cats = [];
  let initial = { t2: [], t1: [] };
  const sel = new Set(); // выбранные подразделы t2
  const t1Sel = new Set();
  const groupBox = h('div', { class: 'groups' });
  const general = h('div', { class: 'error' });

  (async () => {
    let settings;
    try {
      [settings, t1Cats] = await Promise.all([get('/settings'), get('/sources/t1/categories')]);
      canEdit = !!(await get('/status')).canEdit;
    } catch (e) {
      content.replaceChildren(h('p', { class: 'error' }, e.message));
      return;
    }
    initial = { t2: settings.catalog.sections.t2 || [], t1: settings.catalog.sections.t1 || [] };
    for (const id of initial.t1) t1Sel.add(id);
    try {
      const [fullNodes, levelNodes] = await Promise.all([get('/sources/t2/categories'), get('/sources/t2/categories?level=1')]);
      const g = buildTree(levelNodes);
      // Дерева ещё нет (первый запуск без сети) — выбор не показывается и сохранится как был.
      if (g.roots.some((x) => x.children.length > 0)) {
        groups = g;
        for (const id of decodeGroups(buildTree(fullNodes), groups, initial.t2)) sel.add(id);
      }
    } catch {
      groups = null;
    }
    if (alive) draw();
  })();

  function draw() {
    const names = Object.fromEntries(ownTrackers());
    content.replaceChildren(
      canEdit ? '' : remoteNote(),
      general,
      h('div', { class: 'sections-grid' },
        names.t2 ? h('div', { class: 'card' }, h('div', { class: 'h' }, names.t2),
          groups ? groupBox : h('div', { class: 'error' }, 'Разделы не загрузились — выбор не изменится')) : null,
        names.t1 ? h('div', { class: 'card' }, h('div', { class: 'h' }, names.t1),
          h('div', { class: 'list' }, t1Cats.map((c) => h('label', { class: 'check' },
            h('input', { type: 'checkbox', name: `t1-${c.id}`, checked: t1Sel.has(c.id), disabled: !canEdit, 'data-key': `t1-${c.id}`,
              onchange: (e) => (e.target.checked ? t1Sel.add(c.id) : t1Sel.delete(c.id)) }), c.name)))) : null),
    );
    if (groups) drawGroups();
    saveBtn.disabled = !canEdit;
  }

  // drawGroups — группа: флажок «вся группа» (в трёх состояниях) и подразделы под ним.
  function drawGroups() {
    const active = document.activeElement;
    const key = active && groupBox.contains(active) ? active.dataset.key : null;
    groupBox.replaceChildren(...groups.roots.filter((g) => g.children.length > 0).map((g) => {
      const ids = g.children.map((c) => c.id);
      const n = ids.filter((id) => sel.has(id)).length;
      return h('div', { class: 'group' },
        h('label', { class: 'check group-head' },
          h('input', { type: 'checkbox', name: `rt-${g.id}`, checked: n === ids.length, indeterminate: n > 0 && n < ids.length, disabled: !canEdit,
            'data-key': `rt-${g.id}`, onchange: () => {
              const all = n === ids.length;
              for (const id of ids) (all ? sel.delete(id) : sel.add(id));
              drawGroups();
            } }),
          h('span', { class: 'grow' }, g.name),
          n > 0 ? h('span', { class: 'count' }, n === ids.length ? `все ${ids.length}` : `${n} из ${ids.length}`) : null),
        h('div', { class: 'list' }, g.children.map((c) => h('label', { class: 'check' },
          h('input', { type: 'checkbox', name: `rt-${c.id}`, checked: sel.has(c.id), disabled: !canEdit, 'data-key': `rt-${c.id}`, onchange: (e) => {
            if (e.target.checked) sel.add(c.id);
            else sel.delete(c.id);
            drawGroups();
          } }), c.name))));
    }));
    if (key) groupBox.querySelector(`[data-key="${CSS.escape(key)}"]`)?.focus({ preventScroll: true });
  }

  async function save() {
    general.textContent = '';
    saved.textContent = '';
    const sections = {
      // Словарь — целиком (контракт API для пульта): трекер без записи потерял бы разделы.
      t2: groups ? encodeGroups(groups, sel) : initial.t2,
      t1: t1Cats.filter((c) => t1Sel.has(c.id)).map((c) => c.id),
    };
    saveBtn.disabled = true;
    try {
      await put('/settings', { catalog: { sections } });
      saved.textContent = 'Сохранено';
    } catch (e) {
      general.textContent = e.message;
    }
    saveBtn.disabled = !canEdit;
  }
  saveBtn.addEventListener('click', save);

  return () => {
    alive = false;
  };
}

// buildTree — дерево из списка узлов {id, name, parentId}: byId и корни в порядке списка.
export function buildTree(nodes) {
  const byId = new Map(nodes.map((n) => [n.id, { ...n, children: [] }]));
  const roots = [];
  for (const n of byId.values()) {
    const p = byId.get(n.parentId);
    if (p) p.children.push(n);
    else roots.push(n);
  }
  return { byId, roots };
}

// decodeGroups — прежняя строка настройки t2 в выбранные подразделы групп (как NormalizeSections
// сервера): «cN+» — все подразделы группы, подраздел («7», «7+») — он сам, подфорум любой глубины — его
// подраздел первого уровня (по полному дереву full). Чего нет среди подразделов групп — пропускается.
export function decodeGroups(full, groups, entries) {
  const out = new Set();
  const sub = (id) => {
    const n = groups.byId.get(id);
    return !!n && !!n.parentId;
  };
  for (const e of entries) {
    const id = e.endsWith('+') ? e.slice(0, -1) : e;
    const g = groups.byId.get(id);
    if (g && !g.parentId) {
      for (const c of g.children) out.add(c.id);
      continue;
    }
    for (let n = full.byId.get(id) || g; n; n = full.byId.get(n.parentId)) {
      if (sub(n.id)) {
        out.add(n.id);
        break;
      }
    }
  }
  return out;
}

// encodeGroups — выбранное строкой настройки: «подраздел+» (со всеми его подфорумами, и будущими), в
// порядке групп и дерева.
export function encodeGroups(groups, selected) {
  return groups.roots.flatMap((g) => g.children.filter((c) => selected.has(c.id)).map((c) => c.id + '+'));
}
