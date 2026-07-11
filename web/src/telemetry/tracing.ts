import { trace } from "@opentelemetry/api";

export const offlineSyncTracer = trace.getTracer("commute-shield.offline-sync", "0.1.0");
