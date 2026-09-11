import { Banner, Button, LayerCard, SensitiveInput, Text } from "@cloudflare/kumo";
import { Check, UserPlus } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";

import type { OrganizationInvite } from "../app/types";

import { apiClient, errorMessage } from "../app/api";
import { useAuth } from "../app/auth-context";
import { Input } from "../components/ui/input";

type OIDCConfig = {
  directLogin: boolean;
  enabled: boolean;
  loading: boolean;
};

export function InvitePage() {
  const { token = "" } = useParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const [invite, setInvite] = useState<OrganizationInvite | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [signupMode, setSignupMode] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [oidcConfig, setOIDCConfig] = useState<OIDCConfig>({
    directLogin: false,
    enabled: false,
    loading: true,
  });
  const startedDirectLogin = useRef(false);

  useEffect(() => {
    let cancelled = false;
    async function loadInvite() {
      const { data, error } = await apiClient.GET("/v1/invites/{token}", {
        params: { path: { token } },
      });
      if (error || !data) {
        setError(errorMessage(error, "Invite is not available"));
        return;
      }
      const nextInvite: OrganizationInvite = { ...data, scopes: data.scopes ?? [] };
      if (!cancelled) {
        setInvite(nextInvite);
        setEmail(nextInvite.email);
      }
    }
    void loadInvite();
    return () => {
      cancelled = true;
    };
  }, [token]);

  useEffect(() => {
    let cancelled = false;
    async function loadOIDCConfig() {
      try {
        const { data } = await apiClient.GET("/v1/auth/oidc/config");
        if (!cancelled) {
          setOIDCConfig({
            directLogin: Boolean(data?.direct_login),
            enabled: Boolean(data?.enabled),
            loading: false,
          });
        }
      } catch {
        if (!cancelled) {
          setOIDCConfig({ directLogin: false, enabled: false, loading: false });
        }
      }
    }
    void loadOIDCConfig();
    return () => {
      cancelled = true;
    };
  }, []);

  const shouldStartDirectLogin =
    auth.ready &&
    oidcConfig.directLogin &&
    oidcConfig.enabled &&
    !auth.signedIn &&
    !startedDirectLogin.current;

  useEffect(() => {
    if (!shouldStartDirectLogin) return;
    startedDirectLogin.current = true;
    startOIDCLogin();
  }, [shouldStartDirectLogin]);

  if (!token) return <Navigate to="/login" replace />;
  if (!auth.ready || oidcConfig.loading || shouldStartDirectLogin) {
    return <div className="min-h-screen bg-kumo-canvas" />;
  }

  function startOIDCLogin() {
    const params = new URLSearchParams();
    params.set("next", `/invites/${token}`);
    window.location.assign(`/v1/auth/oidc/start?${params.toString()}`);
  }

  async function accept(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      if (!auth.signedIn) {
        if (signupMode) await auth.signup(email, password);
        else await auth.login(email, password);
      }
      const { data, error } = await apiClient.POST("/v1/invites/{token}/accept", {
        params: { path: { token } },
        body: {},
      });
      if (error || !data) throw new Error(errorMessage(error, "Could not accept invite"));
      await auth.refresh();
      auth.setActiveOrgID(data.membership.org_id);
      void navigate("/", { replace: true });
    } catch (err) {
      const message = err instanceof Error ? err.message : "Could not accept invite";
      if (message === "invalid email or password") {
        setError(
          "The email or password is incorrect. Check your credentials, or create a new account.",
        );
      } else if (message === "user already exists") {
        setSignupMode(false);
        setError("An account already exists for this email. Use your existing account to sign in.");
      } else {
        setError(message);
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="min-h-screen bg-kumo-canvas px-5 py-8 md:px-8 md:py-10">
      <div className="flex min-h-[calc(100vh-4rem)] items-center justify-center md:min-h-[calc(100vh-5rem)]">
        <LayerCard className="w-full max-w-[460px] px-5 py-4">
          <form onSubmit={accept}>
            <div className="flex flex-col gap-4">
              <div className="flex items-center gap-3">
                <UserPlus size={22} />
                <Text as="h1" variant="heading3">
                  Join organization
                </Text>
              </div>
              {error && <Banner description={error} variant="error" />}
              {invite && (
                <Text size="sm" variant="secondary">
                  {invite.inviter_email || "A Nanoflare user"} invited {invite.email} to join{" "}
                  {invite.org_name || "this organization"} as {invite.role}.
                </Text>
              )}
              {!auth.signedIn && oidcConfig.enabled && (
                <>
                  <Text size="sm" variant="secondary">
                    Continue with your organization's single sign-on provider. Your account will be
                    created automatically if needed.
                  </Text>
                  <Button
                    className="w-full justify-center"
                    onClick={startOIDCLogin}
                    type="button"
                    variant="secondary"
                  >
                    Sign in with SSO
                  </Button>
                </>
              )}
              {!auth.signedIn && !oidcConfig.enabled && (
                <>
                  <Text size="sm" variant="secondary">
                    {signupMode
                      ? "Create an account to accept this invite."
                      : "Sign in with your existing account to accept this invite."}
                  </Text>
                  <Input
                    autoComplete="email"
                    label="Email"
                    required
                    type="email"
                    value={email}
                    onChange={(event) => setEmail(event.currentTarget.value)}
                  />
                  <SensitiveInput
                    autoComplete={signupMode ? "new-password" : "current-password"}
                    label="Password"
                    required
                    value={password}
                    onChange={(event) => setPassword(event.currentTarget.value)}
                  />
                </>
              )}
              {auth.signedIn && (
                <Text size="sm" variant="secondary">
                  Accept this invite with your signed-in account.
                </Text>
              )}
              {(auth.signedIn || !oidcConfig.enabled) && (
                <Button icon={Check} loading={submitting} type="submit">
                  {auth.signedIn
                    ? "Accept invite"
                    : signupMode
                      ? "Create account and accept"
                      : "Sign in and accept"}
                </Button>
              )}
              {!auth.signedIn && !oidcConfig.enabled && (
                <Button
                  className="w-full justify-center"
                  onClick={() => {
                    setSignupMode((value) => !value);
                    setError("");
                  }}
                  type="button"
                  variant="primary"
                >
                  {signupMode ? "Use existing account" : "Create a new account"}
                </Button>
              )}
            </div>
          </form>
        </LayerCard>
      </div>
    </div>
  );
}
