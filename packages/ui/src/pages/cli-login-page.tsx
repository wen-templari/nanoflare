import { Alert, Box, Button, Code, Group, Loader, Paper, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { Check, Copy, LoaderCircle, Terminal } from "lucide-react";
import { useEffect, useState } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { apiFetch, errorText } from "../app/api";
import { useAuth } from "../app/auth-context";

export function CLILoginPage() {
  const auth = useAuth();
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  const callbackURL = params.get("callback_url") || "";
  const state = params.get("state") || "";
  const [code, setCode] = useState("");
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const [sentToCLI, setSentToCLI] = useState(false);

  useEffect(() => {
    if (!auth.ready || !auth.signedIn || code || error) return;
    let cancelled = false;
    async function createCode() {
      const response = await apiFetch("/v1/auth/cli/code", { method: "POST" });
      if (!response.ok) {
        if (!cancelled) setError(await errorText(response, "Could not create CLI login code"));
        return;
      }
      const payload = await response.json() as { code: string };
      if (!cancelled) setCode(payload.code || "");
    }
    void createCode();
    return () => {
      cancelled = true;
    };
  }, [auth.ready, auth.signedIn, code, error]);

  useEffect(() => {
    if (!code || !callbackURL || !state || sentToCLI) return;
    let callback: URL;
    try {
      callback = new URL(callbackURL);
    } catch {
      setError("CLI callback URL is invalid.");
      return;
    }
    if (!isLoopbackCallback(callback)) {
      setError("CLI callback URL must use localhost or 127.0.0.1.");
      return;
    }
    callback.searchParams.set("code", code);
    callback.searchParams.set("state", state);
    setSentToCLI(true);
    window.location.assign(callback.toString());
  }, [callbackURL, code, sentToCLI, state]);

  if (!auth.ready) return null;
  if (!auth.signedIn) {
    const next = `${location.pathname}${location.search}`;
    return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
  }

  async function copyCode() {
    if (!code) return;
    await navigator.clipboard.writeText(code);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }

  return (
    <Box className="min-h-screen bg-[var(--mantine-color-gray-0)] px-4 py-10">
      <Stack align="center" justify="center" mih="calc(100vh - 5rem)">
        <Box w="100%" maw={520}>
          <Stack gap="lg">
            <Group gap="sm">
              <ThemeIcon size={40} radius="md">
                <Terminal size={20} />
              </ThemeIcon>
              <Box>
                <Title order={1} size="h3">Nanoflare CLI login</Title>
                <Text c="dimmed" size="sm">{auth.userEmail}</Text>
              </Box>
            </Group>
            <Paper bg="white" p="xl" radius="lg" shadow="xs" withBorder>
              <Stack>
                {error && <Alert color="red">{error}</Alert>}
                {!code && !error && (
                  <Group gap="sm">
                    <Loader size="sm" />
                    <Text>Creating login code...</Text>
                  </Group>
                )}
                {code && callbackURL && sentToCLI && (
                  <Group gap="sm">
                    <LoaderCircle className="animate-spin" size={16} />
                    <Text>Returning to Nanoflare CLI...</Text>
                  </Group>
                )}
                {code && !callbackURL && (
                  <>
                    <Text c="dimmed" size="sm">Copy this one-time code back into your terminal.</Text>
                    <Code block fz="lg" p="md">{code}</Code>
                    <Button leftSection={copied ? <Check size={16} /> : <Copy size={16} />} onClick={copyCode} variant="light">
                      {copied ? "Copied" : "Copy code"}
                    </Button>
                  </>
                )}
              </Stack>
            </Paper>
          </Stack>
        </Box>
      </Stack>
    </Box>
  );
}

function isLoopbackCallback(callback: URL) {
  if (callback.protocol !== "http:" && callback.protocol !== "https:") return false;
  return callback.hostname === "127.0.0.1" || callback.hostname === "localhost" || callback.hostname === "::1" || callback.hostname === "[::1]";
}
