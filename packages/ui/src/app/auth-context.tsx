import { createContext, useContext, useEffect, useState } from "react";
import { apiFetch, authToken, clearAuth, saveActiveOrg, saveAuth } from "./api";
import type { AuthSession, Organization } from "./types";

type AuthContextValue = {
  ready: boolean;
  signedIn: boolean;
  userEmail: string;
  organizations: Organization[];
  activeOrgID: string;
  login: (email: string, password: string) => Promise<void>;
  loginWithOIDCCode: (code: string) => Promise<void>;
  signup: (email: string, password: string) => Promise<void>;
  createOrganization: (name: string) => Promise<void>;
  refresh: () => Promise<AuthSession>;
  setActiveOrgID: (orgID: string) => void;
  logout: () => void;
};

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [ready, setReady] = useState(false);
  const [userEmail, setUserEmail] = useState("");
  const [organizations, setOrganizations] = useState<Organization[]>([]);
  const [activeOrgIDState, setActiveOrgIDState] = useState(() => window.localStorage.getItem("nanoflare.auth.active_org_id") || "");

  useEffect(() => {
    let cancelled = false;

    async function hydrate() {
      const token = authToken();
      if (!token) {
        setReady(true);
        return;
      }
      try {
        const session = await refresh();
        if (cancelled) return;
      } catch {
        if (!cancelled) {
          clearAuth();
          setUserEmail("");
          setOrganizations([]);
          setActiveOrgIDState("");
        }
      } finally {
        if (!cancelled) setReady(true);
      }
    }

    void hydrate();
    return () => {
      cancelled = true;
    };
  }, []);

  async function refresh() {
    const response = await apiFetch("/v1/auth/me");
    if (!response.ok) throw new Error("auth expired");
    const session = await response.json() as AuthSession;
    const currentOrgID = window.localStorage.getItem("nanoflare.auth.active_org_id") || "";
    const savedOrgID = currentOrgID && session.organizations.some((org) => org.id === currentOrgID) ? currentOrgID : "";
    const orgID = savedOrgID || session.active_org_id || session.organizations[0]?.id || "";
    setUserEmail(session.user.email);
    setOrganizations(session.organizations);
    setActiveOrgIDState(orgID);
    if (orgID) saveActiveOrg(orgID);
    else window.localStorage.removeItem("nanoflare.auth.active_org_id");
    return session;
  }

  async function authenticate(path: string, email: string, password: string) {
    const response = await fetch(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email, password }),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({ error: "Login failed" }));
      throw new Error(body.error || "Login failed");
    }
    const session = await response.json() as AuthSession;
    const orgID = session.active_org_id || session.organizations[0]?.id || "";
    saveAuth(session.token, orgID);
    setUserEmail(session.user.email);
    setOrganizations(session.organizations);
    setActiveOrgIDState(orgID);
  }

  async function login(email: string, password: string) {
    await authenticate("/v1/auth/login", email, password);
  }

  async function loginWithOIDCCode(code: string) {
    const response = await fetch("/v1/auth/oidc/session", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ code }),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({ error: "OIDC login failed" }));
      throw new Error(body.error || "OIDC login failed");
    }
    const session = await response.json() as AuthSession;
    const orgID = session.active_org_id || session.organizations[0]?.id || "";
    saveAuth(session.token, orgID);
    setUserEmail(session.user.email);
    setOrganizations(session.organizations);
    setActiveOrgIDState(orgID);
  }

  async function signup(email: string, password: string) {
    await authenticate("/v1/auth/signup", email, password);
  }

  async function createOrganization(name: string) {
    const response = await apiFetch("/v1/orgs", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({ error: "Could not create organization" }));
      throw new Error(body.error || "Could not create organization");
    }
    const org = await response.json() as Organization;
    const nextOrgs = [...organizations.filter((item) => item.id !== org.id), org].sort((a, b) => a.name.localeCompare(b.name));
    setOrganizations(nextOrgs);
    setActiveOrgID(org.id);
  }

  function setActiveOrgID(orgID: string) {
    saveActiveOrg(orgID);
    setActiveOrgIDState(orgID);
  }

  function logout() {
    clearAuth();
    setUserEmail("");
    setOrganizations([]);
    setActiveOrgIDState("");
  }

  return (
    <AuthContext.Provider
      value={{
        ready,
        signedIn: Boolean(userEmail),
        userEmail,
        organizations,
        activeOrgID: activeOrgIDState,
        login,
        loginWithOIDCCode,
        signup,
        createOrganization,
        refresh,
        setActiveOrgID,
        logout,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) throw new Error("useAuth must be used inside AuthProvider");
  return context;
}
