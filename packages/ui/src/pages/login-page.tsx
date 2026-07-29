import { Banner, Button, Input, LayerCard, SensitiveInput, Text } from "@cloudflare/kumo";
import { Boxes, LogIn } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { useAuth } from "../app/auth-context";

type OIDCConfig = {
  directLogin: boolean;
  enabled: boolean;
  loading: boolean;
};

export function LoginPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const next = searchParams.get("next") || "/";
  const oidcCode = searchParams.get("oidc_code") || "";
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
  const [oidcLoading, setOIDCLoading] = useState(false);
  const [suppressDirectLogin, setSuppressDirectLogin] = useState(
    () => searchParams.get("sso_logged_out") === "1",
  );
  const handledOIDCCode = useRef("");
  const startedDirectLogin = useRef(false);

  useEffect(() => {
    let cancelled = false;
    async function loadOIDCConfig() {
      const response = await fetch("/v1/auth/oidc/config").catch(() => null);
      if (!response?.ok) {
        if (!cancelled) setOIDCConfig({ directLogin: false, enabled: false, loading: false });
        return;
      }
      const config = (await response
        .json()
        .catch(() => ({ direct_login: false, enabled: false }))) as {
        direct_login?: boolean;
        enabled?: boolean;
      };
      if (!cancelled) {
        setOIDCConfig({
          directLogin: Boolean(config.direct_login),
          enabled: Boolean(config.enabled),
          loading: false,
        });
      }
    }
    void loadOIDCConfig();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!oidcCode) return;
    if (handledOIDCCode.current === oidcCode) return;
    handledOIDCCode.current = oidcCode;
    let cancelled = false;
    async function completeOIDCLogin() {
      setOIDCLoading(true);
      setError("");
      try {
        await auth.loginWithOIDCCode(oidcCode);
        if (!cancelled) navigate(next, { replace: true });
      } catch (err) {
        if (!cancelled) {
          handledOIDCCode.current = "";
          setSuppressDirectLogin(true);
          setError(err instanceof Error ? err.message : "OIDC login failed");
          const params = new URLSearchParams(searchParams);
          params.delete("oidc_code");
          setSearchParams(params, { replace: true });
        }
      } finally {
        if (!cancelled) setOIDCLoading(false);
      }
    }
    void completeOIDCLogin();
    return () => {
      cancelled = true;
    };
  }, [auth, navigate, next, oidcCode, searchParams, setSearchParams]);

  const shouldStartDirectLogin =
    oidcConfig.directLogin &&
    oidcConfig.enabled &&
    !auth.signedIn &&
    !oidcCode &&
    !suppressDirectLogin;

  useEffect(() => {
    if (oidcConfig.loading || !shouldStartDirectLogin || startedDirectLogin.current) return;
    startedDirectLogin.current = true;
    setOIDCLoading(true);
    startOIDCLogin();
  }, [oidcConfig.loading, shouldStartDirectLogin]);

  if (auth.signedIn) return <Navigate to={next} replace />;
  if ((oidcConfig.loading && !oidcCode) || shouldStartDirectLogin) {
    return <div className="min-h-screen bg-kumo-base" />;
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      if (signupMode) await auth.signup(email, password);
      else await auth.login(email, password);
      navigate(next, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
    } finally {
      setSubmitting(false);
    }
  }

  function startOIDCLogin() {
    const params = new URLSearchParams();
    params.set("next", next);
    window.location.assign(`/v1/auth/oidc/start?${params.toString()}`);
  }

  return (
    <div className="min-h-screen bg-kumo-base px-4 py-10">
      <div className="flex min-h-[calc(100vh-5rem)] items-center justify-center">
        <div className="w-full max-w-[420px]">
          <div className="flex flex-col gap-6">
            <div className="flex items-center gap-3">
              <div className="grid size-10 place-items-center rounded-lg bg-kumo-info/15 text-kumo-info">
                <Boxes size={20} />
              </div>
              <Text as="h1" variant="heading3">
                nanoflare
              </Text>
            </div>
            <LayerCard>
              <form onSubmit={submit}>
                <div className="flex flex-col gap-4">
                  {error && <Banner description={error} variant="error" />}
                  <div>
                    <Text as="h2" variant="heading3">
                      {signupMode ? "Create account" : "Sign in"}
                    </Text>
                    <Text size="sm" variant="secondary">
                      {signupMode
                        ? "Create your Nanoflare account. You can create or join an organization next."
                        : "Use your control-plane account."}
                    </Text>
                  </div>
                  <Input
                    autoComplete="email"
                    label="Email"
                    onChange={(event) => setEmail(event.currentTarget.value)}
                    required
                    type="email"
                    value={email}
                  />
                  <SensitiveInput
                    autoComplete={signupMode ? "new-password" : "current-password"}
                    label="Password"
                    onChange={(event) => setPassword(event.currentTarget.value)}
                    required
                    value={password}
                  />
                  <Button icon={LogIn} loading={submitting} type="submit">
                    {signupMode ? "Create account" : "Sign in"}
                  </Button>
                  {oidcConfig.enabled && !signupMode && (
                    <>
                      <div className="flex items-center gap-3">
                        <div className="h-px flex-1 bg-kumo-line" />
                        <Text size="sm" variant="secondary">
                          or
                        </Text>
                        <div className="h-px flex-1 bg-kumo-line" />
                      </div>
                      <Button
                        icon={LogIn}
                        loading={oidcLoading}
                        onClick={startOIDCLogin}
                        type="button"
                        variant="secondary"
                      >
                        Sign in with OIDC
                      </Button>
                    </>
                  )}
                  <Button onClick={() => setSignupMode((value) => !value)} variant="ghost">
                    {signupMode ? "Use existing account" : "Create a new account"}
                  </Button>
                </div>
              </form>
            </LayerCard>
          </div>
        </div>
      </div>
    </div>
  );
}
