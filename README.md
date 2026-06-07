# Project CommuteShield Monorepo (Web-First PWA)

## Architecture Rulebook for AI Coding Agents

This project is an Offline-First, Low-Data Carpooling web application built for mobile browsers in the Nigerian infrastructure landscape.

### System Constraints

1. **Offline-First via IndexedDB**: The user interface interacts directly with the browser's IndexedDB (via Dexie.js).
2. **Background Sync**: Network operations must run asynchronously inside Service Workers to handle volatile internet drops.
3. **Data Serialization**: Real-time communication must prioritize binary wire layouts over heavy text JSON streams.

### Structure Mapping

- `/backend` -> Go microservice tracking spatial queries.
- `/web` -> Next.js PWA client utilizing TypeScript and Tailwind CSS.
- `/supabase` -> Raw PostgreSQL + PostGIS structural schema files.

### To Spin Up Local Dev Testing Environment

```bash
docker-compose up -d
```

---
