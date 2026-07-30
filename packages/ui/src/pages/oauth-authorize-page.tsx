import { Banner, Button, LayerCard, Select, Text } from "@cloudflare/kumo";
import { Boxes, Check, ShieldCheck } from "lucide-react";
import { Navigate, useLocation } from "react-router-dom";
import { useEffect, useMemo, useState } from "react";
import { apiFetch, errorText } from "../app/api";
import { useAuth } from "../app/auth-context";

type AuthorizeResponse = {
  redirect_to: string;
};

type AuthorizeInfo = {
  client_id: string;
  client_name: string;
  redirect_uri: string;
  scopes: string[];
};

export function OAuthAuthorizePage() {
  const auth = useAuth();
  const location = useLocation();
  const params = useMemo(() => new URLSearchParams(location.search), [location.search]);
  const [orgID, setOrgID] = useState(() => auth.activeOrgID);
  const [clientInfo, setClientInfo] = useState<AuthorizeInfo | null>(null);
  const [loadingInfo, setLoadingInfo] = useState(true);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const clientID = params.get("client_id") || "";
  const redirectURI = params.get("redirect_uri") || "";
  const state = params.get("state") || "";
  const scopes = (params.get("scope") || "")
    .split(/\s+/)
    .map((scope) => scope.trim())
    .filter(Boolean);

  useEffect(() => {
    let cancelled = false;

    async function verifyClient() {
      setLoadingInfo(true);
      setError("");
      const query = new URLSearchParams();
      query.set("client_id", clientID);
      query.set("redirect_uri", redirectURI);
      query.set("scope", scopes.join(" "));
      try {
        const response = await fetch(`/v1/oauth/authorize?${query.toString()}`, {
          headers: { accept: "application/json" },
        });
        if (!response.ok)
          throw new Error(await errorText(response, "Could not verify external app"));
        const info = (await response.json()) as AuthorizeInfo;
        if (!cancelled) setClientInfo(info);
      } catch (err) {
        if (!cancelled) {
          setClientInfo(null);
          setError(err instanceof Error ? err.message : "Could not verify external app");
        }
      } finally {
        if (!cancelled) setLoadingInfo(false);
      }
    }

    void verifyClient();
    return () => {
      cancelled = true;
    };
  }, [clientID, redirectURI, location.search]);

  if (!auth.ready) return null;

  if (!auth.signedIn) {
    const next = encodeURIComponent(`${location.pathname}${location.search}`);
    return <Navigate to={`/login?next=${next}`} replace />;
  }

  async function approve() {
    setSubmitting(true);
    setError("");
    try {
      const response = await apiFetch("/v1/oauth/authorize", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          client_id: clientID,
          redirect_uri: redirectURI,
          scopes: clientInfo?.scopes ?? scopes,
          state,
          org_id: orgID,
        }),
      });
      if (!response.ok) throw new Error(await errorText(response, "Could not approve connection"));
      const payload = (await response.json()) as AuthorizeResponse;
      window.location.assign(payload.redirect_to);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not approve connection");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="min-h-screen bg-kumo-base px-5 py-8 md:px-8 md:py-10">
      <div className="flex min-h-[calc(100vh-4rem)] items-center justify-center md:min-h-[calc(100vh-5rem)]">
        <div className="w-full max-w-[560px]">
          <div className="flex flex-col gap-6">
            <div className="flex items-center gap-3">
              <div className="grid size-10 place-items-center rounded-lg bg-kumo-info/15 text-kumo-info">
                <Boxes size={20} />
              </div>
              <div>
                <Text as="h1" variant="heading3">
                  nanoflare
                </Text>
                <Text size="sm" variant="secondary">
                  External app connection
                </Text>
              </div>
            </div>
            <LayerCard className="px-5 py-4">
              <div className="flex flex-col gap-4">
                {error && <Banner description={error} variant="error" />}
                <div className="flex items-start gap-3">
                  <div className="grid size-[42px] shrink-0 place-items-center rounded-full bg-kumo-success/15 text-kumo-success">
                    <ShieldCheck size={22} />
                  </div>
                  <div>
                    <Text as="h2" variant="heading3">
                      Approve access
                    </Text>
                    <Text size="sm" variant="secondary">
                      An external app is requesting access to manage resources in one Nanoflare
                      organization.
                    </Text>
                  </div>
                </div>

                <div>
                  <Text as="p" variant="heading3">
                    {loadingInfo ? "Verifying..." : clientInfo?.client_name || "Unknown app"}
                  </Text>
                  <Text DANGEROUS_className="text-xs" variant="mono-secondary">
                    {clientInfo?.client_id || clientID || "Missing client_id"}
                  </Text>
                </div>

                <Select
                  items={auth.organizations.map((org) => ({ value: org.id, label: org.name }))}
                  label="Nanoflare organization"
                  onValueChange={(value) => value && setOrgID(value)}
                  value={orgID}
                />

                <div>
                  <Text as="h3" DANGEROUS_className="mb-1.5" size="sm">
                    Requested permissions
                  </Text>
                  <div className="flex flex-wrap gap-1.5">
                    {(clientInfo?.scopes ?? scopes).length > 0 ? (
                      (clientInfo?.scopes ?? scopes).map((scope) => (
                        <span
                          className="rounded bg-kumo-info/15 px-2 py-1 text-sm text-kumo-info"
                          key={scope}
                        >
                          {scope}
                        </span>
                      ))
                    ) : (
                      <Text DANGEROUS_className="text-kumo-danger" size="sm">
                        No scopes requested
                      </Text>
                    )}
                  </div>
                </div>

                <div>
                  <Text as="h3" size="sm">
                    Redirect URI
                  </Text>
                  <Text DANGEROUS_className="text-xs" variant="mono-secondary">
                    {clientInfo?.redirect_uri || redirectURI || "Missing redirect_uri"}
                  </Text>
                </div>

                <div className="flex justify-end gap-2">
                  <Button onClick={() => window.close()} variant="ghost">
                    Cancel
                  </Button>
                  <Button
                    disabled={!clientInfo || loadingInfo}
                    icon={Check}
                    loading={submitting}
                    onClick={approve}
                  >
                    Approve and return
                  </Button>
                </div>
              </div>
            </LayerCard>
          </div>
        </div>
      </div>
    </div>
  );
}
