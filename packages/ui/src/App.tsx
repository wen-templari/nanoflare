import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { WorkspaceProvider } from "./app/workspace-context";
import { ConsoleLayout } from "./components/layout/console-layout";
import { KVNamespaceDetailPage } from "./pages/kv-namespace-detail-page";
import { KVNamespacesPage } from "./pages/kv-namespaces-page";
import { ObjectStorageBucketDetailPage } from "./pages/object-storage-bucket-detail-page";
import { ObjectStorageBucketsPage } from "./pages/object-storage-buckets-page";
import { OverviewPage } from "./pages/overview-page";
import { WorkerDetailPage } from "./pages/worker-detail-page";
import { WorkersPage } from "./pages/workers-page";

export function App() {
  return (
    <WorkspaceProvider>
      <BrowserRouter>
        <Routes>
          <Route element={<ConsoleLayout />}>
            <Route index element={<OverviewPage />} />
            <Route path="workers" element={<WorkersPage />} />
            <Route path="workers/:workerId" element={<WorkerDetailPage />} />
            <Route path="kv" element={<KVNamespacesPage />} />
            <Route path="kv/:namespaceId" element={<KVNamespaceDetailPage />} />
            <Route path="object-storage" element={<ObjectStorageBucketsPage />} />
            <Route path="object-storage/:bucketId" element={<ObjectStorageBucketDetailPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </WorkspaceProvider>
  );
}
