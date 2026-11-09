// Названия трекеров — с сервера (/status, trackerNames): программа трекеров не знает, название даёт профиль
// адреса, который ввёл человек, или скачанное описание трекера (docs/data-packs-and-updates.md, раздел 3).
const names = {};

// OWN_IDS — трекеры со своим разбором и своим каталогом (номера движков, не названия).
export const OWN_IDS = ['t2', 't1'];

export function setTrackerNames(m) {
  for (const k of Object.keys(names)) delete names[k];
  Object.assign(names, m || {});
}

// trackerName — название для человека; неизвестный — как пришёл, с заглавной.
export function trackerName(id) {
  if (!id) return '';
  if (id === 'trackers') return 'Трекеры';
  return names[id] || id[0].toUpperCase() + id.slice(1);
}

// ownTrackers — подключённые трекеры со своим каталогом: [номер, название].
export function ownTrackers() {
  return OWN_IDS.filter((id) => names[id]).map((id) => [id, names[id]]);
}
