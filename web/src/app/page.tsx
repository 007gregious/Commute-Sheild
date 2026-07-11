import { OfflineSyncStatus } from "@/components/OfflineSyncStatus";

export default function HomePage() {
  return (
    <main>
      <section className="card">
        <p>Progressive Web App Framework</p>
        <h1>Commute Shield is ready for resilient offline execution.</h1>
        <p>
          Rides and bookings are staged in IndexedDB, synchronized over gRPC-Web, and retried with
          capped exponential backoff during cellular connectivity drops.
        </p>
        <OfflineSyncStatus />
      </section>
    </main>
  );
}
