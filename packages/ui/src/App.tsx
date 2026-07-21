import { MantineProvider, createTheme } from "@mantine/core";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider, useAuth } from "./app/auth-context";
import { WorkspaceProvider } from "./app/workspace-context";
import { ConsoleLayout } from "./components/layout/console-layout";
import { DatabaseDetailPage } from "./pages/database-detail-page";
import { DatabasesPage } from "./pages/databases-page";
import { KVNamespaceDetailPage } from "./pages/kv-namespace-detail-page";
import { KVNamespacesPage } from "./pages/kv-namespaces-page";
import { ObjectStorageBucketDetailPage } from "./pages/object-storage-bucket-detail-page";
import { ObjectStorageBucketsPage } from "./pages/object-storage-buckets-page";
import { OAuthAuthorizePage } from "./pages/oauth-authorize-page";
import { OverviewPage } from "./pages/overview-page";
import { SettingsPage } from "./pages/settings-page";
import { OAuthClientDetailPage } from "./pages/oauth-client-detail-page";
import { WorkerDetailPage } from "./pages/worker-detail-page";
import { WorkersPage } from "./pages/workers-page";
import { LoginPage } from "./pages/login-page";
import { InvitePage } from "./pages/invite-page";
import { CLILoginPage } from "./pages/cli-login-page";

const theme = createTheme({
  primaryColor: "blue",
  defaultRadius: "md",
  fontFamily: "Manrope, sans-serif",
  headings: { fontFamily: "Manrope, sans-serif" },
});

export function App() {
  return (
    <MantineProvider theme={theme} defaultColorScheme="light">
      <AuthProvider>
        <BrowserRouter>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/cli-login" element={<CLILoginPage />} />
            <Route path="/invites/:token" element={<InvitePage />} />
            <Route path="/oauth/authorize" element={<OAuthAuthorizePage />} />
            <Route element={<ProtectedConsole />}>
              <Route index element={<OverviewPage />} />
              <Route path="workers" element={<WorkersPage />} />
              <Route path="workers/:workerId" element={<WorkerDetailPage />} />
              <Route path="kv" element={<KVNamespacesPage />} />
              <Route path="kv/:namespaceId" element={<KVNamespaceDetailPage />} />
              <Route path="databases" element={<DatabasesPage />} />
              <Route path="databases/:databaseId" element={<DatabaseDetailPage />} />
              <Route path="object-storage" element={<ObjectStorageBucketsPage />} />
              <Route path="object-storage/:bucketId" element={<ObjectStorageBucketDetailPage />} />
              <Route path="settings" element={<SettingsPage />} />
              <Route path="settings/oauth-clients/:clientId" element={<OAuthClientDetailPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </AuthProvider>
    </MantineProvider>
  );
}

function ProtectedConsole() {
  const auth = useAuth();
  const location = useLocation();
  if (!auth.ready) return null;
  if (!auth.signedIn) {
    const next = `${location.pathname}${location.search}${location.hash}`;
    return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
  }
  return (
    <WorkspaceProvider>
      <ConsoleLayout />
    </WorkspaceProvider>
  );
}
