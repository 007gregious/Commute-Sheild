import Dexie, { type EntityTable } from "dexie";

export type SyncStatus = "pending" | "syncing" | "synced" | "failed";
export type OfflineEntityType = "ride" | "booking";

const SYNCING_STALE_AFTER_MS = 10 * 60_000;

export interface OfflineRideOrBooking {
  id: string;
  entityType: OfflineEntityType;
  payload: Record<string, unknown>;
  sync_status: SyncStatus;
  backoffAttempt: number;
  updatedAt: string;
  createdAt: string;
}

export class CommuteShieldDb extends Dexie {
  ridesAndBookings!: EntityTable<OfflineRideOrBooking, "id">;

  constructor() {
    super("commute-shield-offline");
    this.version(1).stores({
      ridesAndBookings: "id, entityType, sync_status, updatedAt"
    });
  }
}

export const commuteDb = new CommuteShieldDb();

function staleSyncingCutoff() {
  return new Date(Date.now() - SYNCING_STALE_AFTER_MS).toISOString();
}

export async function getPendingRidesAndBookings() {
  const staleCutoff = staleSyncingCutoff();

  return commuteDb.transaction("rw", commuteDb.ridesAndBookings, async () => {
    await commuteDb.ridesAndBookings
      .where("sync_status")
      .equals("syncing")
      .and((record) => record.updatedAt < staleCutoff)
      .modify({ sync_status: "pending", updatedAt: new Date().toISOString() });

    return commuteDb.ridesAndBookings.where("sync_status").equals("pending").toArray();
  });
}

export async function markSyncingAtomically(id: string) {
  return commuteDb.transaction("rw", commuteDb.ridesAndBookings, async () => {
    const record = await commuteDb.ridesAndBookings.get(id);
    if (!record || record.sync_status !== "pending") {
      return false;
    }

    await commuteDb.ridesAndBookings.update(id, {
      sync_status: "syncing",
      updatedAt: new Date().toISOString()
    });
    return true;
  });
}

export async function markSyncedAtomically(id: string) {
  await commuteDb.transaction("rw", commuteDb.ridesAndBookings, async () => {
    await commuteDb.ridesAndBookings.update(id, {
      sync_status: "synced",
      backoffAttempt: 0,
      updatedAt: new Date().toISOString()
    });
  });
}

export async function updateBackoffAttempt(id: string, backoffAttempt: number) {
  await commuteDb.ridesAndBookings.update(id, {
    sync_status: "pending",
    backoffAttempt,
    updatedAt: new Date().toISOString()
  });
}

export async function markSyncFailed(id: string) {
  await commuteDb.ridesAndBookings.update(id, {
    sync_status: "failed",
    updatedAt: new Date().toISOString()
  });
}
