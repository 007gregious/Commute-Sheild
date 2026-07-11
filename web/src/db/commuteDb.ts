import Dexie, { type EntityTable } from "dexie";

export type SyncStatus = "pending" | "syncing" | "synced" | "failed";
export type OfflineEntityType = "ride" | "booking";

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

export async function getPendingRidesAndBookings() {
  return commuteDb.ridesAndBookings.where("sync_status").equals("pending").toArray();
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
