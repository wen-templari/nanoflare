import { Alert, Badge, Box, Button, Group, Paper, Select, Stack, Text, ThemeIcon, Title } from "@mantine/core"
import { Boxes, Check, ShieldCheck } from "lucide-react"
import { Navigate, useLocation } from "react-router-dom"
import { useEffect, useMemo, useState } from "react"
import { apiFetch, errorText } from "../app/api"
import { useAuth } from "../app/auth-context"

type AuthorizeResponse = {
  redirect_to: string
}

type AuthorizeInfo = {
  client_id: string
  client_name: string
  redirect_uri: string
  scopes: string[]
}

export function OAuthAuthorizePage() {
  const auth = useAuth()
  const location = useLocation()
  const params = useMemo(() => new URLSearchParams(location.search), [location.search])
  const [orgID, setOrgID] = useState(() => auth.activeOrgID)
  const [clientInfo, setClientInfo] = useState<AuthorizeInfo | null>(null)
  const [loadingInfo, setLoadingInfo] = useState(true)
  const [error, setError] = useState("")
  const [submitting, setSubmitting] = useState(false)

  const clientID = params.get("client_id") || ""
  const redirectURI = params.get("redirect_uri") || ""
  const state = params.get("state") || ""
  const scopes = (params.get("scope") || "")
    .split(/\s+/)
    .map((scope) => scope.trim())
    .filter(Boolean)

  useEffect(() => {
    let cancelled = false

    async function verifyClient() {
      setLoadingInfo(true)
      setError("")
      const query = new URLSearchParams()
      query.set("client_id", clientID)
      query.set("redirect_uri", redirectURI)
      query.set("scope", scopes.join(" "))
      try {
        const response = await fetch(`/v1/oauth/authorize?${query.toString()}`, {
          headers: { accept: "application/json" },
        })
        if (!response.ok) throw new Error(await errorText(response, "Could not verify external app"))
        const info = await response.json() as AuthorizeInfo
        if (!cancelled) setClientInfo(info)
      } catch (err) {
        if (!cancelled) {
          setClientInfo(null)
          setError(err instanceof Error ? err.message : "Could not verify external app")
        }
      } finally {
        if (!cancelled) setLoadingInfo(false)
      }
    }

    void verifyClient()
    return () => {
      cancelled = true
    }
  }, [clientID, redirectURI, location.search])

  if (!auth.ready) return null

  if (!auth.signedIn) {
    const next = encodeURIComponent(`${location.pathname}${location.search}`)
    return <Navigate to={`/login?next=${next}`} replace />
  }

  async function approve() {
    setSubmitting(true)
    setError("")
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
      })
      if (!response.ok) throw new Error(await errorText(response, "Could not approve connection"))
      const payload = await response.json() as AuthorizeResponse
      window.location.assign(payload.redirect_to)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not approve connection")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Box className="min-h-screen bg-[var(--mantine-color-gray-0)] px-4 py-10">
      <Stack align="center" justify="center" mih="calc(100vh - 5rem)">
        <Box w="100%" maw={560}>
          <Stack gap="lg">
            <Group gap="sm">
              <ThemeIcon size={40} radius="md">
                <Boxes size={20} />
              </ThemeIcon>
              <Box>
                <Title order={1} size="h3">nanoflare</Title>
                <Text c="dimmed" size="sm">External app connection</Text>
              </Box>
            </Group>
            <Paper bg="white" p="xl" radius="lg" shadow="xs" withBorder>
              <Stack>
                {error && <Alert color="red">{error}</Alert>}
                <Group align="flex-start" justify="space-between">
                  <ThemeIcon color="green" radius="xl" size={42} variant="light">
                    <ShieldCheck size={22} />
                  </ThemeIcon>
                  <Box>
                    <Title order={2} size="h3">Approve access</Title>
                    <Text c="dimmed" size="sm">
                      An external app is requesting access to manage resources in one Nanoflare organization.
                    </Text>
                  </Box>
                </Group>

                <Box>
                  <Text fw={700} size="lg">{loadingInfo ? "Verifying..." : clientInfo?.client_name || "Unknown app"}</Text>
                  <Text c="dimmed" ff="monospace" size="xs">{clientInfo?.client_id || clientID || "Missing client_id"}</Text>
                </Box>

                <Select
                  allowDeselect={false}
                  data={auth.organizations.map((org) => ({ value: org.id, label: org.name }))}
                  label="Nanoflare organization"
                  onChange={(value) => value && setOrgID(value)}
                  value={orgID}
                />

                <Box>
                  <Text fw={700} mb={6} size="sm">Requested permissions</Text>
                  <Group gap={6}>
                    {(clientInfo?.scopes ?? scopes).length > 0
                      ? (clientInfo?.scopes ?? scopes).map((scope) => <Badge key={scope} variant="light">{scope}</Badge>)
                      : <Text c="red" size="sm">No scopes requested</Text>}
                  </Group>
                </Box>

                <Box>
                  <Text fw={700} size="sm">Redirect URI</Text>
                  <Text c="dimmed" ff="monospace" size="xs">{clientInfo?.redirect_uri || redirectURI || "Missing redirect_uri"}</Text>
                </Box>

                <Group justify="flex-end">
                  <Button color="gray" onClick={() => window.close()} variant="subtle">
                    Cancel
                  </Button>
                  <Button disabled={!clientInfo || loadingInfo} leftSection={<Check size={16} />} loading={submitting} onClick={approve}>
                    Approve and return
                  </Button>
                </Group>
              </Stack>
            </Paper>
          </Stack>
        </Box>
      </Stack>
    </Box>
  )
}
