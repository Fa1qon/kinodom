// Запросы к API Kinodom: JSON туда и обратно, ошибки — текстом для человека.

// ApiError — ответ с ошибкой: status 0 — сервер не ответил.
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

function deviceId() {
  const key = 'kinodom.deviceId';
  try {
    const saved = localStorage.getItem(key);
    if (saved) return saved;
    const id = globalThis.crypto?.randomUUID?.() || Array.from({ length: 24 }, () => Math.floor(Math.random() * 36).toString(36)).join('');
    localStorage.setItem(key, id);
    return id;
  } catch {
    return '';
  }
}

// api — запрос к /api/v1. Изменяющие запросы — всегда с Content-Type: application/json, даже без
// тела: без него сервер отвечает 415 (основная спека, раздел 13).
export async function api(method, path, body) {
  const init = { method, headers: {} };
  const id = deviceId();
  if (id) init.headers['X-Kinodom-Device'] = id;
  if (method !== 'GET') {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body ?? {});
  }
  let resp;
  try {
    resp = await fetch('/api/v1' + path, init);
  } catch {
    throw new ApiError(0, 'Сервер Kinodom не отвечает');
  }
  if (resp.status === 204) return null;
  const text = await resp.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!resp.ok) throw new ApiError(resp.status, (data && data.error) || `Ошибка сервера: ${resp.status}`);
  return data;
}

export const get = (path) => api('GET', path);
export const post = (path, body) => api('POST', path, body);
export const put = (path, body) => api('PUT', path, body);
export const del = (path) => api('DELETE', path);
