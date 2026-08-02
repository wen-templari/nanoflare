import { Banner, Button, Code, LayerCard, Loader, Select, Text } from "@cloudflare/kumo";
import { Check, Command, Copy, LoaderCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { Navigate, useLocation } from "react-router-dom";

import { apiClient, errorMessage } from "../app/api";
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
  const [selectedOrgID, setSelectedOrgID] = useState("");
  const [creatingCode, setCreatingCode] = useState(false);

  useEffect(() => {
    if (!auth.ready || selectedOrgID || !auth.activeOrgID) return;
    setSelectedOrgID(auth.activeOrgID);
  }, [auth.activeOrgID, auth.ready, selectedOrgID]);

  async function createCode() {
    if (creatingCode || code || error) return;
    setCreatingCode(true);
    const { data, error: requestError } = await apiClient.POST("/v1/auth/cli/code", {
      headers: selectedOrgID ? { "X-Nanoflare-Org-ID": selectedOrgID } : undefined,
    });
    if (requestError || !data) {
      setError(errorMessage(requestError, "Could not create CLI login code"));
    } else {
      setCode(data.code || "");
    }
    setCreatingCode(false);
  }

  useEffect(() => {
    if (!auth.ready || !auth.signedIn || code || error || creatingCode) return;
    if (auth.organizations.length === 0) void createCode();
  }, [auth.ready, auth.signedIn, auth.organizations.length, code, error, creatingCode]);

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
    <div className="grid min-h-screen place-items-center bg-kumo-canvas px-5 py-8 md:px-8 md:py-10">
      <div className="w-full max-w-[520px]">
        <div className="flex flex-col gap-6">
          <div className="flex items-center gap-3">
            <div className="grid size-10 place-items-center rounded-lg bg-kumo-info/15 text-kumo-info">
              <Command size={20} />
            </div>
            <div>
              <Text as="h1" variant="heading3">
                Nanoflare CLI login
              </Text>
              <Text size="sm" variant="secondary">
                Select organization
              </Text>
            </div>
          </div>
          <LayerCard className="bg-white px-5 py-4 shadow-none">
            <div className="flex flex-col gap-4">
              {error && <Banner description={error} variant="error" />}
              {!code && !error && creatingCode && (
                <div className="flex items-center gap-3">
                  <Loader size="sm" />
                  <Text>Creating login code...</Text>
                </div>
              )}
              {!code && !error && !creatingCode && auth.organizations.length > 0 && (
                <div className="flex flex-col gap-4">
                  <Text size="sm" variant="secondary">
                    Choose the organization to use with the CLI.
                  </Text>
                  <Select
                    aria-label="Organization for CLI login"
                    className="w-full"
                    items={auth.organizations.map((org) => ({ value: org.id, label: org.name }))}
                    label="Organization"
                    onValueChange={(value) => value && setSelectedOrgID(value)}
                    placeholder="Select an organization"
                    value={selectedOrgID}
                  />
                  <div className="flex justify-end">
                    <Button disabled={!selectedOrgID} onClick={() => void createCode()}>
                      Continue
                    </Button>
                  </div>
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
