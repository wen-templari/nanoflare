import createClient from "openapi-fetch";
import type { paths } from "@nanoflare/schema";

const tokenKey = "nanoflare.auth.token";
const activeOrgKey = "nanoflare.auth.active_org_id";

/** Generated OpenAPI client for every JSON control-plane request. */
export const apiClient = createClient<paths>();

apiClient.use({
  onRequest({ request }) {
    const headers = new Headers(request.headers);
    const token = authToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    return new Request(request, { headers });
  },
});

export function authToken() {
  return window.localStorage.getItem(tokenKey) || "";
}

export function activeOrgID() {
  return window.localStorage.getItem(activeOrgKey) || "";
}

export function saveAuth(token: string, orgID: string) {
  window.localStorage.setItem(tokenKey, token);
  if (orgID) window.localStorage.setItem(activeOrgKey, orgID);
  else window.localStorage.removeItem(activeOrgKey);
}

export function saveActiveOrg(orgID: string) {
  window.localStorage.setItem(activeOrgKey, orgID);
}

export function clearAuth() {
  window.localStorage.removeItem(tokenKey);
  window.localStorage.removeItem(activeOrgKey);
}

export function errorMessage(
  error: { detail?: string; error?: string } | undefined,
  fallback: string,
) {
  return error?.detail || error?.error || fallback;
}

/** Performs requests for runtime data routes that are not part of the control-plane schema. */
export function apiFetch(input: RequestInfo | URL, init?: RequestInit) {
  const headers = new Headers(init?.headers);
  const token = authToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return fetch(input, { ...init, headers });
}

export async function errorText(response: Response, fallback: string) {
  const text = await response.text();
  return text || fallback;
}

export async function fetchJSON<T>(input: RequestInfo | URL, init?: RequestInit): Promise<T> {
  const response = await apiFetch(input, init);
  if (!response.ok) throw new Error(await errorText(response, `Request failed (${response.status})`));
  return (await response.json()) as T;
}
