// «Настройки → Разделы каталога»: Rutracker — деревом с флажками в трёх состояниях и поиском,
// Rutor — списком (спека этапа 7, разделы 5.4 и 6.3).
import { h, icon } from '../ui.js';
import { get, put } from '../api.js';
import { layout, remoteNote } from './settings-layout.js';

export function render(root, r, ctx) {
  const saveBtn = h('button', { class: 'btn inv', type: 'button', 'data-key': 'save' }, icon('save'), 'Сохранить');
  const saved = h('span', { class: 'muted small', role: 'status' });
  const content = layout(root, 'sections', 'Разделы каталога', saved, saveBtn);
  let alive = true;
  let canEdit = false;
  let tree = null; // дерево Rutracker; null — не загрузилось
  let rutorCats = [];
  let initial = { rutracker: [], rutor: [] };
  const sel = new Set(); // выбранные разделы Rutracker (у раздела с подразделами — его собственные раздачи)
  const rutorSel = new Set();
  const expanded = new Set();
  let query = '';
  const treeBox = h('div', { role: 'tree', 'aria-label': 'Разделы Rutracker', class: 'tree' });
  const general = h('div', { class: 'error' });

  (async () => {
    let settings;
    try {
      [settings, rutorCats] = await Promise.all([get('/settings'), get('/sources/rutor/categories')]);
      canEdit = !!(await get('/status')).canEdit;
    } catch (e) {
      content.replaceChildren(h('p', { class: 'error' }, e.message));
      return;
    }
    initial = { rutracker: settings.catalog.sections.rutracker || [], rutor: settings.catalog.sections.rutor || [] };
    for (const id of initial.rutor) rutorSel.add(id);
    try {
      tree = buildTree(await get('/sources/rutracker/categories'));
      const d = decodeSections(tree, initial.rutracker);
      for (const u of d.selected) sel.add(u);
      for (const id of d.expanded) expanded.add(id);
    } catch {
      tree = null; // без дерева раздел Rutracker не меняется — сохранится как был
    }
    if (alive) draw();
  })();


  function draw() {
    content.replaceChildren(
      canEdit ? '' : remoteNote(),
      general,
      h('div', { class: 'sections-grid' },
        h('div', { class: 'card' },
          h('div', { class: 'row' }, h('div', { class: 'h grow' }, 'Rutracker'),
            tree ? h('label', { class: 'field small-field' }, icon('search', 18),
              h('input', { placeholder: 'Найти раздел', 'aria-label': 'Найти раздел', value: query, 'data-key': 'filter', oninput: (e) => {
                query = e.target.value.trim().toLowerCase();
                drawTree();
              } })) : null),
          tree ? treeBox : h('div', { class: 'error' }, 'Дерево разделов Rutracker не загрузилось — раздел не изменится')),
        h('div', { class: 'card' }, h('div', { class: 'h' }, 'Rutor'),
          h('div', { class: 'list' }, rutorCats.map((c) => h('label', { class: 'check' },
            h('input', { type: 'checkbox', name: `rutor-${c.id}`, checked: rutorSel.has(c.id), disabled: !canEdit, 'data-key': `rutor-${c.id}`,
              onchange: (e) => (e.target.checked ? rutorSel.add(c.id) : rutorSel.delete(c.id)) }), c.name))))),
    );
    if (tree) drawTree();
    saveBtn.disabled = !canEdit;
  }

  function drawTree() {
    const active = document.activeElement;
    const key = active && treeBox.contains(active) ? active.dataset.key : null;
    const rows = [];
    const matches = query ? matching() : null;
    const walk = (node, depth) => {
      if (matches && !matches.has(node.id)) return;
      rows.push(row(node, depth));
      if (!node.children.length || !(expanded.has(node.id) || (matches && query))) return;
      if (!node.id.startsWith('c') && (!matches || node.name.toLowerCase().includes(query))) rows.push(ownRow(node, depth + 1));
      for (const c of node.children) walk(c, depth + 1);
    };
    for (const n of tree.roots) walk(n, 0);
    treeBox.replaceChildren(...rows);
    if (key) treeBox.querySelector(`[data-key="${CSS.escape(key)}"]`)?.focus({ preventScroll: true });
  }

  // matching — узлы, которые видны при поиске: подходят по названию сами или через потомка.
  function matching() {
    const out = new Set();
    const walk = (n) => {
      let hit = n.name.toLowerCase().includes(query);
      for (const c of n.children) if (walk(c)) hit = true;
      if (hit) out.add(n.id);
      return hit;
    };
    tree.roots.forEach(walk);
    return out;
  }

  function row(node, depth) {
    const us = units(node);
    const n = us.filter((u) => sel.has(u)).length;
    const open = expanded.has(node.id) || !!query;
    return h('div', { class: 'node', role: 'treeitem', 'aria-level': depth + 1, 'aria-expanded': node.children.length ? String(open) : null,
      style: { paddingLeft: `${depth * 22}px` } },
    node.children.length
      ? h('button', { class: 'chev', type: 'button', 'aria-label': open ? 'Свернуть' : 'Развернуть', 'data-key': `chev-${node.id}`, onclick: () => {
        if (expanded.has(node.id)) expanded.delete(node.id);
        else expanded.add(node.id);
        drawTree();
      } }, icon(open ? 'expand_more' : 'chevron_right'))
      : h('span', { class: 'chev' }),
    h('label', { class: 'node-label' },
      h('input', { type: 'checkbox', name: `rt-${node.id}`, checked: n === us.length && n > 0, indeterminate: n > 0 && n < us.length, disabled: !canEdit,
        'data-key': `rt-${node.id}`, onchange: () => {
          const all = n === us.length;
          for (const u of us) (all ? sel.delete(u) : sel.add(u));
          drawTree();
        } }),
      h('span', { class: 'ellipsis' }, node.name)),
    node.children.length && n > 0 ? h('span', { class: 'count' }, n === us.length ? `все ${us.length}` : `${n} из ${us.length}`) : null);
  }

  // ownRow — «<раздел> — без подразделов»: собственные раздачи раздела, у которого есть подразделы.
  function ownRow(node, depth) {
    return h('div', { class: 'node', role: 'treeitem', 'aria-level': depth + 1, style: { paddingLeft: `${depth * 22}px` } },
      h('span', { class: 'chev' }),
      h('label', { class: 'node-label' },
        h('input', { type: 'checkbox', name: `rt-own-${node.id}`, checked: sel.has(node.id), disabled: !canEdit, 'data-key': `rt-own-${node.id}`, onchange: (e) => {
          if (e.target.checked) sel.add(node.id);
          else sel.delete(node.id);
          drawTree();
        } }),
        h('span', { class: 'ellipsis muted' }, `${node.name} — без подразделов`)));
  }


  async function save() {
    general.textContent = '';
    saved.textContent = '';
    const sections = {
      // Словарь — целиком (контракт API для пульта): трекер без записи потерял бы разделы.
      rutracker: tree ? encodeSections(tree, sel) : initial.rutracker,
      rutor: rutorCats.filter((c) => rutorSel.has(c.id)).map((c) => c.id),
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

// buildTree — дерево из списка узлов {id, name, parentId}; units(узел) — выбираемые разделы под ним.
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

const unitCache = new WeakMap();

// units — выбираемые разделы под узлом: сам раздел (у категорий своих раздач нет) и все подразделы.
export function units(node) {
  if (unitCache.has(node)) return unitCache.get(node);
  const out = node.id.startsWith('c') ? [] : [node.id];
  for (const c of node.children) out.push(...units(c));
  unitCache.set(node, out);
  return out;
}

// decodeSections — строка настройки Rutracker в выбранные разделы: «раздел+» и «cN+» — всё под
// узлом, «раздел» — только он сам. expanded — узлы на пути к выбранному: видно, что отмечено.
// Записи, которых нет в дереве, пропускаются (сервер их не примет).
export function decodeSections(tree, entries) {
  const selected = new Set();
  const expanded = new Set();
  for (const e of entries) {
    const all = e.endsWith('+');
    const node = tree.byId.get(all ? e.slice(0, -1) : e);
    if (!node) continue;
    if (all) for (const u of units(node)) selected.add(u);
    else selected.add(node.id);
    for (let p = tree.byId.get(node.parentId); p; p = tree.byId.get(p.parentId)) expanded.add(p.id);
  }
  return { selected, expanded };
}

// encodeSections — выбранное строкой настройки: узел, отмеченный целиком, — «раздел+» (с будущими
// подразделами), категория — «cN+»; иначе — сам раздел и отмеченные подразделы по отдельности.
export function encodeSections(tree, selected) {
  const encode = (node) => {
    const us = units(node);
    const n = us.filter((u) => selected.has(u)).length;
    if (n === 0) return [];
    if (n === us.length) return [node.children.length ? node.id + '+' : node.id];
    const out = !node.id.startsWith('c') && selected.has(node.id) ? [node.id] : [];
    for (const c of node.children) out.push(...encode(c));
    return out;
  };
  return tree.roots.flatMap(encode);
}
