import { Toasty } from "@cloudflare/kumo";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { AuthProvider, useAuth } from "./app/auth-context";
import { appToastManager } from "./app/toast";
import { WorkspaceProvider } from "./app/workspace-context";
import { ConsoleLayout } from "./components/layout/console-layout";
import { CLILoginPage } from "./pages/cli-login-page";
import { DatabaseDetailPage } from "./pages/database-detail-page";
import { DatabaseExplorerPage } from "./pages/database-explorer-page";
import { DatabasesPage } from "./pages/databases-page";
import { InvitePage } from "./pages/invite-page";
import { KVNamespaceDetailPage } from "./pages/kv-namespace-detail-page";
import { KVNamespacesPage } from "./pages/kv-namespaces-page";
import { LoginPage } from "./pages/login-page";
import { OAuthAuthorizePage } from "./pages/oauth-authorize-page";
import { OAuthClientDetailPage } from "./pages/oauth-client-detail-page";
import { ObjectStorageBucketDetailPage } from "./pages/object-storage-bucket-detail-page";
import { ObjectStorageBucketsPage } from "./pages/object-storage-buckets-page";
import { ObjectStorageObjectDetailPage } from "./pages/object-storage-object-detail-page";
import { OverviewPage } from "./pages/overview-page";
import { SettingsPage } from "./pages/settings-page";
import { WorkerDetailPage } from "./pages/worker-detail-page";
import { WorkersPage } from "./pages/workers-page";

export function App() {
  return (
    <Toasty toastManager={appToastManager}>
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
              <Route path="databases/:databaseId/explore" element={<DatabaseExplorerPage />} />
              <Route path="databases/:databaseId" element={<DatabaseDetailPage />} />
              <Route path="object-storage" element={<ObjectStorageBucketsPage />} />
              <Route path="object-storage/:bucketId" element={<ObjectStorageBucketDetailPage />} />
              <Route
                path="object-storage/:bucketId/objects/*"
                element={<ObjectStorageObjectDetailPage />}
              />
              <Route path="settings" element={<SettingsPage />} />
              <Route path="settings/oauth-clients/:clientId" element={<OAuthClientDetailPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </AuthProvider>
    </Toasty>
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
