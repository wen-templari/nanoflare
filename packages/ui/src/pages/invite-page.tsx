import { Banner, Button, LayerCard, SensitiveInput, Text } from "@cloudflare/kumo";
import { Check, UserPlus } from "lucide-react";
import { useEffect, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";

import type { OrganizationInvite } from "../app/types";

import { apiClient, errorMessage } from "../app/api";
import { useAuth } from "../app/auth-context";
import { Input } from "../components/ui/input";

export function InvitePage() {
  const { token = "" } = useParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const [invite, setInvite] = useState<OrganizationInvite | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

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

  if (!token) return <Navigate to="/login" replace />;

  async function accept(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      if (!auth.signedIn) await auth.signup(email, password);
      const { error } = await apiClient.POST("/v1/invites/{token}/accept", {
        params: { path: { token } },
        body: {},
      });
      if (error) throw new Error(errorMessage(error, "Could not accept invite"));
      await auth.refresh();
      void navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not accept invite");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="min-h-screen bg-kumo-base px-5 py-8 md:px-8 md:py-10">
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
              {!auth.signedIn && (
                <>
                  <Input
                    autoComplete="email"
                    label="Email"
                    required
                    type="email"
                    value={email}
                    onChange={(event) => setEmail(event.currentTarget.value)}
                  />
                  <SensitiveInput
                    autoComplete="new-password"
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
              <Button icon={Check} loading={submitting} type="submit">
                Accept invite
              </Button>
            </div>
          </form>
        </LayerCard>
      </div>
    </div>
  );
}
