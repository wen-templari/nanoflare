import { Alert, Box, Button, Group, Paper, PasswordInput, Stack, Text, TextInput, ThemeIcon, Title } from "@mantine/core";
import { Boxes, LogIn } from "lucide-react";
import { useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { useAuth } from "../app/auth-context";

export function LoginPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [organizationName, setOrganizationName] = useState("");
  const [setupMode, setSetupMode] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  if (auth.signedIn) return <Navigate to="/" replace />;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      await auth.login(email, password, setupMode ? organizationName : undefined);
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
    } finally {
      setSubmitting(false);
    }
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
                <Text c="dimmed" size="sm">Control plane access</Text>
              </Box>
            </Group>
            <Paper bg="white" p="xl" radius="lg" shadow="xs" withBorder>
              <form onSubmit={submit}>
                <Stack>
                  {error && <Alert color="red">{error}</Alert>}
                  <Box>
                    <Title order={2} size="h4">{setupMode ? "First time setup" : "Sign in"}</Title>
                    <Text c="dimmed" size="sm">
                      {setupMode ? "Create the first user and organization." : "Use your control-plane account."}
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
                    autoComplete={setupMode ? "new-password" : "current-password"}
                    label="Password"
                    onChange={(event) => setPassword(event.currentTarget.value)}
                    required
                    value={password}
                  />
                  {setupMode && (
                    <TextInput
                      label="Organization"
                      onChange={(event) => setOrganizationName(event.currentTarget.value)}
                      required
                      value={organizationName}
                    />
                  )}
                  <Button leftSection={<LogIn size={16} />} loading={submitting} type="submit">
                    {setupMode ? "Create account" : "Sign in"}
                  </Button>
                  <Button color="gray" onClick={() => setSetupMode((value) => !value)} variant="subtle">
                    {setupMode ? "Use existing account" : "Set up a new control plane"}
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
