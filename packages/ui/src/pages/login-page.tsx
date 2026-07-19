import { Alert, Box, Button, Divider, Group, Paper, PasswordInput, Stack, Text, TextInput, ThemeIcon, Title } from "@mantine/core";
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
  const [oidcConfig, setOIDCConfig] = useState<OIDCConfig>({ directLogin: false, enabled: false, loading: true });
  const [oidcLoading, setOIDCLoading] = useState(false);
  const [suppressDirectLogin, setSuppressDirectLogin] = useState(() => searchParams.get("sso_logged_out") === "1");
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
      const config = await response.json().catch(() => ({ direct_login: false, enabled: false })) as { direct_login?: boolean; enabled?: boolean };
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

  const shouldStartDirectLogin = oidcConfig.directLogin && oidcConfig.enabled && !auth.signedIn && !oidcCode && !suppressDirectLogin;

  useEffect(() => {
    if (oidcConfig.loading || !shouldStartDirectLogin || startedDirectLogin.current) return;
    startedDirectLogin.current = true;
    setOIDCLoading(true);
    startOIDCLogin();
  }, [oidcConfig.loading, shouldStartDirectLogin]);

  if (auth.signedIn) return <Navigate to={next} replace />;
  if ((oidcConfig.loading && !oidcCode) || shouldStartDirectLogin) {
    return <Box className="min-h-screen bg-[var(--mantine-color-gray-0)]" />;
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
    <Box className="min-h-screen bg-[var(--mantine-color-gray-0)] px-4 py-10">
      <Stack align="center" justify="center" mih="calc(100vh - 5rem)">
        <Box w="100%" maw={420}>
          <Stack gap="lg">
            <Group gap="sm">
              <ThemeIcon size={40} radius="md">
                <Boxes size={20} />
              </ThemeIcon>
              <Box>
                <Title order={1} size="h3">nanoflare</Title>
              </Box>
            </Group>
            <Paper bg="white" p="xl" radius="lg" shadow="xs" withBorder>
              <form onSubmit={submit}>
                <Stack>
                  {error && <Alert color="red">{error}</Alert>}
                  <Box>
                    <Title order={2} size="h4">{signupMode ? "Create account" : "Sign in"}</Title>
                    <Text c="dimmed" size="sm">
                      {signupMode ? "Create your Nanoflare account. You can create or join an organization next." : "Use your control-plane account."}
                    </Text>
                  </Box>
                  <TextInput
                    autoComplete="email"
                    label="Email"
                    onChange={(event) => setEmail(event.currentTarget.value)}
                    required
                    type="email"
                    value={email}
                  />
                  <PasswordInput
                    autoComplete={signupMode ? "new-password" : "current-password"}
                    label="Password"
                    onChange={(event) => setPassword(event.currentTarget.value)}
                    required
                    value={password}
                  />
                  <Button leftSection={<LogIn size={16} />} loading={submitting} type="submit">
                    {signupMode ? "Create account" : "Sign in"}
                  </Button>
                  {oidcConfig.enabled && !signupMode && (
                    <>
                      <Divider label="or" labelPosition="center" />
                      <Button leftSection={<LogIn size={16} />} loading={oidcLoading} onClick={startOIDCLogin} type="button" variant="light">
                        Sign in with OIDC
                      </Button>
                    </>
                  )}
                  <Button color="gray" onClick={() => setSignupMode((value) => !value)} variant="subtle">
                    {signupMode ? "Use existing account" : "Create a new account"}
                  </Button>
                </Stack>
              </form>
            </Paper>
          </Stack>
        </Box>
      </Stack>
    </Box>
  );
}
