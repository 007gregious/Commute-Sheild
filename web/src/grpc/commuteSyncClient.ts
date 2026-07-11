import type { OfflineRideOrBooking } from "@/db/commuteDb";

export interface SyncDeliveryResponse {
  accepted: boolean;
  clusterNode?: string;
}

export interface GrpcWebSyncClient {
  deliverOfflineRecord(record: OfflineRideOrBooking, signal?: AbortSignal): Promise<SyncDeliveryResponse>;
}

class FetchGrpcWebSyncClient implements GrpcWebSyncClient {
  constructor(private readonly endpoint = process.env.NEXT_PUBLIC_GRPC_WEB_SYNC_URL ?? "/api/grpc/sync") {}

  async deliverOfflineRecord(record: OfflineRideOrBooking, signal?: AbortSignal): Promise<SyncDeliveryResponse> {
    const response = await fetch(this.endpoint, {
      method: "POST",
      headers: {
        "content-type": "application/grpc-web+json",
        "x-grpc-web": "1"
      },
      body: JSON.stringify({
        id: record.id,
        type: record.entityType,
        payload: record.payload
      }),
      signal
    });

    if (!response.ok) {
      throw new Error(`gRPC-Web sync failed with HTTP ${response.status}`);
    }

    return (await response.json()) as SyncDeliveryResponse;
  }
}

export const commuteSyncClient: GrpcWebSyncClient = new FetchGrpcWebSyncClient();
