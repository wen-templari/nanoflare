import { createContext, useContext, useEffect, useState } from "react";

import type { AuthSession, Organization } from "./types";

import { apiClient, authToken, clearAuth, saveActiveOrg, saveAuth } from "./api";

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
  const [activeOrgIDState, setActiveOrgIDState] = useState(
    () => window.localStorage.getItem("nanoflare.auth.active_org_id") || "",
  );

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
    const { data, error } = await apiClient.GET("/v1/auth/me", { parseAs: "json" });
    if (error || !data) throw new Error("auth expired");
    // The server currently documents this legacy endpoint as an object map.
    const session: AuthSession = {
      ...data,
      organizations: (data.organizations ?? []).map((organization) => ({
        ...organization,
        scopes: organization.scopes ?? undefined,
      })),
    };
    const currentOrgID = window.localStorage.getItem("nanoflare.auth.active_org_id") || "";
    const savedOrgID =
      currentOrgID && session.organizations.some((org) => org.id === currentOrgID)
        ? currentOrgID
        : "";
    const orgID = savedOrgID || session.active_org_id || session.organizations[0]?.id || "";
    setUserEmail(session.user.email);
    setOrganizations(session.organizations);
    setActiveOrgIDState(orgID);
    if (orgID) saveActiveOrg(orgID);
    else window.localStorage.removeItem("nanoflare.auth.active_org_id");
    return session;
  }

  async function authenticate(
    path: "/v1/auth/login" | "/v1/auth/signup",
    email: string,
    password: string,
  ) {
    const result =
      path === "/v1/auth/login"
        ? await apiClient.POST("/v1/auth/login", { body: { email, password }, parseAs: "json" })
        : await apiClient.POST("/v1/auth/signup", { body: { email, password }, parseAs: "json" });
    if (result.error || !result.data) throw new Error(result.error?.error || "Login failed");
    const session = result.data as AuthSession;
    const orgID = session.active_org_id || session.organizations[0]?.id || "";
    saveAuth(session.access_token, orgID);
    setUserEmail(session.user.email);
    setOrganizations(session.organizations);
    setActiveOrgIDState(orgID);
  }

  async function login(email: string, password: string) {
    await authenticate("/v1/auth/login", email, password);
  }

  async function loginWithOIDCCode(code: string) {
    const { data, error } = await apiClient.POST("/v1/auth/oidc/session", {
      body: { code },
      parseAs: "json",
    });
    if (error || !data) throw new Error(error?.error || "OIDC login failed");
    const session = data as AuthSession;
    const orgID = session.active_org_id || session.organizations[0]?.id || "";
    saveAuth(session.access_token, orgID);
    setUserEmail(session.user.email);
    setOrganizations(session.organizations);
    setActiveOrgIDState(orgID);
  }

  async function signup(email: string, password: string) {
    await authenticate("/v1/auth/signup", email, password);
  }

  async function createOrganization(name: string) {
    const { data, error } = await apiClient.POST("/v1/organizations", {
      body: { name },
      parseAs: "json",
    });
    if (error || !data) throw new Error(error?.error || "Could not create organization");
    const org = data as Organization;
    const nextOrgs = [...organizations.filter((item) => item.id !== org.id), org].sort((a, b) =>
      a.name.localeCompare(b.name),
    );
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
