import { createContext, useContext, useEffect, useState } from "react";
import { apiFetch, authToken, clearAuth, saveActiveOrg, saveAuth } from "./api";
import type { AuthSession, Organization } from "./types";

type AuthContextValue = {
  ready: boolean;
  signedIn: boolean;
  userEmail: string;
  organizations: Organization[];
  activeOrgID: string;
  login: (email: string, password: string, organizationName?: string) => Promise<void>;
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
        const response = await apiFetch("/v1/auth/me");
        if (!response.ok) throw new Error("auth expired");
        const session = await response.json() as AuthSession;
        if (cancelled) return;
        const orgID = activeOrgIDState || session.active_org_id || session.organizations[0]?.id || "";
        setUserEmail(session.user.email);
        setOrganizations(session.organizations);
        setActiveOrgIDState(orgID);
        if (orgID) saveActiveOrg(orgID);
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

  async function login(email: string, password: string, organizationName?: string) {
    const payload = organizationName
      ? { email, password, organization_name: organizationName }
      : { email, password };
    const path = organizationName ? "/v1/setup/signup" : "/v1/auth/login";
    const response = await fetch(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(payload),
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
