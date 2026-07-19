"use client";

import { SpanStatusCode } from "@opentelemetry/api";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  getPendingRidesAndBookings,
  markSyncFailed,
  markSyncedAtomically,
  markSyncingAtomically,
  updateBackoffAttempt
} from "@/db/commuteDb";
import { commuteSyncClient, type GrpcWebSyncClient } from "@/grpc/commuteSyncClient";
import { offlineSyncTracer } from "@/telemetry/tracing";

const RETRY_DELAYS_MS = [5_000, 30_000, 120_000];
const MAX_RETRY_DELAY_MS = 15 * 60_000;

function nextRetryDelay(attempt: number) {
  if (attempt < RETRY_DELAYS_MS.length) return RETRY_DELAYS_MS[attempt];
  const scaledDelay = RETRY_DELAYS_MS[RETRY_DELAYS_MS.length - 1] * 2 ** (attempt - RETRY_DELAYS_MS.length + 1);
  return Math.min(scaledDelay, MAX_RETRY_DELAY_MS);
}

function isConnectivityDrop(error: unknown) {
  return !navigator.onLine || error instanceof TypeError || (error instanceof DOMException && error.name === "AbortError");
}

export function useResilientOfflineSync(client: GrpcWebSyncClient = commuteSyncClient) {
  const [isSyncing, setIsSyncing] = useState(false);
  const [pendingCount, setPendingCount] = useState(0);
  const [lastError, setLastError] = useState<Error | null>(null);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const inFlight = useRef(false);

  const clearTimers = useCallback(() => {
    timers.current.forEach(clearTimeout);
    timers.current = [];
  }, []);

  const synchronize = useCallback(async () => {
    if (inFlight.current) {
      console.info("[offline-sync] sync already in progress; skipping overlapping request");
      return;
    }

    inFlight.current = true;
    clearTimers();
    const span = offlineSyncTracer.startSpan("offline.sync.flushPendingRecords");
    setIsSyncing(true);

    try {
      const pendingRecords = await getPendingRidesAndBookings();
      setPendingCount(pendingRecords.length);
      span.setAttribute("offline.pending.count", pendingRecords.length);
      console.info("[offline-sync] querying pending rides/bookings", { count: pendingRecords.length });

      for (const record of pendingRecords) {
        const recordSpan = offlineSyncTracer.startSpan("offline.sync.deliverRecord", {
          attributes: {
            "offline.record.id": record.id,
            "offline.record.type": record.entityType,
            "offline.record.backoffAttempt": record.backoffAttempt
          }
        });

        try {
          const claimed = await markSyncingAtomically(record.id);
          if (!claimed) {
            console.info("[offline-sync] record was claimed by another sync pass; skipping", { id: record.id });
            continue;
          }

          console.info("[offline-sync] delivering record via gRPC-Web", { id: record.id, type: record.entityType });
          await client.deliverOfflineRecord(record);
          await markSyncedAtomically(record.id);
          recordSpan.setStatus({ code: SpanStatusCode.OK });
          console.info("[offline-sync] record synced and IndexedDB status atomically updated", { id: record.id });
        } catch (error) {
          const deliveryError = error instanceof Error ? error : new Error(String(error));
          recordSpan.recordException(deliveryError);
          recordSpan.setStatus({ code: SpanStatusCode.ERROR, message: deliveryError.message });
          setLastError(deliveryError);

          if (isConnectivityDrop(error)) {
            const nextAttempt = record.backoffAttempt + 1;
            const delay = nextRetryDelay(record.backoffAttempt);
            await updateBackoffAttempt(record.id, nextAttempt);
            console.warn("[offline-sync] cellular/network drop detected; scheduling retry", {
              id: record.id,
              nextAttempt,
              delayMs: delay
            });
            timers.current.push(setTimeout(() => void synchronize(), delay));
          } else {
            await markSyncFailed(record.id);
            console.error("[offline-sync] non-retryable gRPC-Web delivery failure", deliveryError);
          }
        } finally {
          recordSpan.end();
        }
      }

      const remaining = await getPendingRidesAndBookings();
      setPendingCount(remaining.length);
      span.setStatus({ code: SpanStatusCode.OK });
    } catch (error) {
      const syncError = error instanceof Error ? error : new Error(String(error));
      span.recordException(syncError);
      span.setStatus({ code: SpanStatusCode.ERROR, message: syncError.message });
      setLastError(syncError);
      console.error("[offline-sync] failed to query or process IndexedDB queue", syncError);
    } finally {
      span.end();
      inFlight.current = false;
      setIsSyncing(false);
    }
  }, [clearTimers, client]);

  useEffect(() => {
    void synchronize();
    window.addEventListener("online", synchronize);
    return () => {
      window.removeEventListener("online", synchronize);
      clearTimers();
    };
  }, [clearTimers, synchronize]);

  return { isSyncing, pendingCount, lastError, triggerSync: synchronize };
}
