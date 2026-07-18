import { Alert, Box, Button, Group, Paper, PasswordInput, Stack, Text, TextInput, Title } from "@mantine/core";
import { Check, UserPlus } from "lucide-react";
import { useEffect, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { apiFetch, errorText } from "../app/api";
import { useAuth } from "../app/auth-context";
import type { OrganizationInvite } from "../app/types";

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
      const response = await fetch(`/v1/invites/${token}`);
      if (!response.ok) {
        setError(await errorText(response, "Invite is not available"));
        return;
      }
      const nextInvite = await response.json() as OrganizationInvite;
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
      const response = await apiFetch(`/v1/invites/${token}/accept`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
      if (!response.ok) throw new Error(await errorText(response, "Could not accept invite"));
      await auth.refresh();
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not accept invite");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Box className="min-h-screen bg-[var(--mantine-color-gray-0)] px-4 py-10">
      <Stack align="center" justify="center" mih="calc(100vh - 5rem)">
        <Paper bg="white" p="xl" radius="lg" shadow="xs" withBorder w="100%" maw={460}>
          <form onSubmit={accept}>
            <Stack>
              <Group>
                <UserPlus size={22} />
                <Title order={1} size="h3">Join organization</Title>
              </Group>
              {error && <Alert color="red">{error}</Alert>}
              {invite && (
                <Text c="dimmed" size="sm">
                  {invite.inviter_email || "A Nanoflare user"} invited {invite.email} to join {invite.org_name || "this organization"} as {invite.role}.
                </Text>
              )}
              {!auth.signedIn && (
                <>
                  <TextInput autoComplete="email" label="Email" required type="email" value={email} onChange={(event) => setEmail(event.currentTarget.value)} />
                  <PasswordInput autoComplete="new-password" label="Password" required value={password} onChange={(event) => setPassword(event.currentTarget.value)} />
                </>
              )}
              {auth.signedIn && <Text c="dimmed" size="sm">Accept this invite with your signed-in account.</Text>}
              <Button leftSection={<Check size={16} />} loading={submitting} type="submit">Accept invite</Button>
            </Stack>
          </form>
        </Paper>
      </Stack>
    </Box>
  );
}
