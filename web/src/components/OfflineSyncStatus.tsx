"use client";

import { useResilientOfflineSync } from "@/hooks/useResilientOfflineSync";

export function OfflineSyncStatus() {
  const { isSyncing, pendingCount, lastError, triggerSync } = useResilientOfflineSync();

  return (
    <div aria-live="polite">
      <p>Pending local records: {pendingCount}</p>
      <p>Sync state: {isSyncing ? "Synchronizing" : "Idle"}</p>
      {lastError ? <p role="alert">Last sync error: {lastError.message}</p> : null}
      <button type="button" onClick={triggerSync} disabled={isSyncing}>
        Sync now
      </button>
    </div>
  );
}
