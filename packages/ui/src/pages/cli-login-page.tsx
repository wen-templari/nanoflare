import { Banner, Button, Code, LayerCard, Loader, Text } from "@cloudflare/kumo";
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
      const payload = (await response.json()) as { code: string };
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
    <div className="min-h-screen bg-kumo-base px-5 py-8 md:px-8 md:py-10">
      <div className="flex min-h-[calc(100vh-4rem)] items-center justify-center md:min-h-[calc(100vh-5rem)]">
        <div className="w-full max-w-[520px]">
          <div className="flex flex-col gap-6">
            <div className="flex items-center gap-3">
              <div className="grid size-10 place-items-center rounded-lg bg-kumo-info/15 text-kumo-info">
                <Terminal size={20} />
              </div>
              <div>
                <Text as="h1" variant="heading3">
                  Nanoflare CLI login
                </Text>
                <Text size="sm" variant="secondary">
                  {auth.userEmail}
                </Text>
              </div>
            </div>
            <LayerCard className="px-5 py-4">
              <div className="flex flex-col gap-4">
                {error && <Banner description={error} variant="error" />}
                {!code && !error && (
                  <div className="flex items-center gap-3">
                    <Loader size="sm" />
                    <Text>Creating login code...</Text>
                  </div>
                )}
                {code && callbackURL && sentToCLI && (
                  <div className="flex items-center gap-3">
                    <LoaderCircle className="animate-spin" size={16} />
                    <Text>Returning to Nanoflare CLI...</Text>
                  </div>
                )}
                {code && !callbackURL && (
                  <>
                    <Text size="sm" variant="secondary">
                      Copy this one-time code back into your terminal.
                    </Text>
                    <Code.Block code={code} />
                    <Button icon={copied ? Check : Copy} onClick={copyCode} variant="secondary">
                      {copied ? "Copied" : "Copy code"}
                    </Button>
                  </>
                )}
              </div>
            </LayerCard>
          </div>
        </div>
      </div>
    </div>
  );
}

function isLoopbackCallback(callback: URL) {
  if (callback.protocol !== "http:" && callback.protocol !== "https:") return false;
  return (
    callback.hostname === "127.0.0.1" ||
    callback.hostname === "localhost" ||
    callback.hostname === "::1" ||
    callback.hostname === "[::1]"
  );
}
