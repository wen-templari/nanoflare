const tokenKey = "nanoflare.auth.token";
const activeOrgKey = "nanoflare.auth.active_org_id";

export function authToken() {
  return window.localStorage.getItem(tokenKey) || "";
}

export function activeOrgID() {
  return window.localStorage.getItem(activeOrgKey) || "";
}

export function saveAuth(token: string, orgID: string) {
  window.localStorage.setItem(tokenKey, token);
  if (orgID) window.localStorage.setItem(activeOrgKey, orgID);
}

export function saveActiveOrg(orgID: string) {
  window.localStorage.setItem(activeOrgKey, orgID);
}

export function clearAuth() {
  window.localStorage.removeItem(tokenKey);
  window.localStorage.removeItem(activeOrgKey);
}

export async function apiFetch(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  const token = authToken();
  const orgID = activeOrgID();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (orgID) headers.set("X-Nanoflare-Org-ID", orgID);
  return fetch(path, { ...init, headers });
}

export async function fetchJSON<T>(path: string) {
  const response = await apiFetch(path);
  if (!response.ok) throw new Error(`Request failed: ${response.status}`);
  return response.json() as Promise<T>;
}

export async function errorText(response: Response, fallback: string) {
  try {
    const payload = await response.json() as { error?: string };
    return payload.error || fallback;
  } catch {
    return fallback;
  }
}
