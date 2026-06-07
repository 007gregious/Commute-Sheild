-- CommuteShield PostGIS schema and route-matching query.
-- This migration is intentionally declarative/idempotent so it can be replayed
-- by Supabase/PostgreSQL migration tooling without relying on CONCURRENTLY.

create extension if not exists postgis;
create extension if not exists pgcrypto;

create schema if not exists commute;

do $$
begin
    create type commute.user_role as enum ('passenger', 'driver', 'admin');
exception
    when duplicate_object then null;
end;
$$;

do $$
begin
    create type commute.route_status as enum ('scheduled', 'in_progress', 'completed', 'cancelled');
exception
    when duplicate_object then null;
end;
$$;

do $$
begin
    create type commute.request_status as enum ('open', 'matched', 'cancelled', 'expired');
exception
    when duplicate_object then null;
end;
$$;

create table if not exists commute.profiles (
    id uuid primary key default gen_random_uuid(),
    full_name text not null,
    phone_number text not null unique,
    role commute.user_role not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint profiles_phone_number_not_blank check (length(btrim(phone_number)) > 0),
    constraint profiles_full_name_not_blank check (length(btrim(full_name)) > 0)
);

create table if not exists commute.drivers (
    id uuid primary key default gen_random_uuid(),
    profile_id uuid not null unique references commute.profiles(id) on delete cascade,
    vehicle_make text not null,
    vehicle_model text not null,
    license_plate text not null unique,
    seat_capacity integer not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint drivers_seat_capacity_positive check (seat_capacity > 0),
    constraint drivers_license_plate_not_blank check (length(btrim(license_plate)) > 0)
);

create table if not exists commute.passengers (
    id uuid primary key default gen_random_uuid(),
    profile_id uuid not null unique references commute.profiles(id) on delete cascade,
    accessibility_notes text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists commute.driver_routes (
    id uuid primary key default gen_random_uuid(),
    driver_id uuid not null references commute.drivers(id) on delete cascade,
    origin_label text not null,
    destination_label text not null,
    departure_time timestamptz not null,
    route_path geometry(LineString, 4326) not null,
    available_seats integer not null,
    fare_amount numeric(12, 2) not null default 0,
    status commute.route_status not null default 'scheduled',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint driver_routes_available_seats_non_negative check (available_seats >= 0),
    constraint driver_routes_fare_amount_non_negative check (fare_amount >= 0),
    constraint driver_routes_route_path_linestring check (geometrytype(route_path) = 'LINESTRING'),
    constraint driver_routes_route_path_srid check (st_srid(route_path) = 4326),
    constraint driver_routes_route_path_min_points check (st_npoints(route_path) >= 2)
);

create table if not exists commute.ride_requests (
    id uuid primary key default gen_random_uuid(),
    passenger_id uuid not null references commute.passengers(id) on delete cascade,
    pickup_label text not null,
    dropoff_label text not null,
    pickup_lat double precision not null,
    pickup_lon double precision not null,
    dropoff_lat double precision not null,
    dropoff_lon double precision not null,
    earliest_departure timestamptz,
    latest_departure timestamptz,
    seats_requested integer not null default 1,
    status commute.request_status not null default 'open',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ride_requests_pickup_lat_valid check (pickup_lat between -90 and 90),
    constraint ride_requests_dropoff_lat_valid check (dropoff_lat between -90 and 90),
    constraint ride_requests_pickup_lon_valid check (pickup_lon between -180 and 180),
    constraint ride_requests_dropoff_lon_valid check (dropoff_lon between -180 and 180),
    constraint ride_requests_seats_requested_positive check (seats_requested > 0),
    constraint ride_requests_departure_window_valid check (
        earliest_departure is null
        or latest_departure is null
        or earliest_departure <= latest_departure
    )
);

create table if not exists commute.route_matches (
    id uuid primary key default gen_random_uuid(),
    route_id uuid not null references commute.driver_routes(id) on delete cascade,
    ride_request_id uuid not null references commute.ride_requests(id) on delete cascade,
    pickup_distance_meters double precision not null,
    dropoff_distance_meters double precision not null,
    matched_at timestamptz not null default now(),
    accepted_at timestamptz,
    unique (route_id, ride_request_id),
    constraint route_matches_pickup_distance_non_negative check (pickup_distance_meters >= 0),
    constraint route_matches_dropoff_distance_non_negative check (dropoff_distance_meters >= 0)
);

-- Declarative migration-compatible indexes. Do not use CREATE INDEX CONCURRENTLY
-- inside schema migrations because it cannot run in a transaction block.
create index if not exists idx_profiles_role on commute.profiles (role);
create index if not exists idx_drivers_profile_id on commute.drivers (profile_id);
create index if not exists idx_passengers_profile_id on commute.passengers (profile_id);
create index if not exists idx_driver_routes_driver_id on commute.driver_routes (driver_id);
create index if not exists idx_driver_routes_open_departure
    on commute.driver_routes (departure_time, available_seats)
    where status = 'scheduled' and available_seats > 0;
create index if not exists idx_driver_routes_route_path_geography_gist
    on commute.driver_routes using gist ((route_path::geography));
create index if not exists idx_ride_requests_passenger_id on commute.ride_requests (passenger_id);
create index if not exists idx_ride_requests_open_departure
    on commute.ride_requests (earliest_departure, latest_departure, seats_requested)
    where status = 'open';
create index if not exists idx_ride_requests_pickup_geography_gist
    on commute.ride_requests using gist ((st_setsrid(st_makepoint(pickup_lon, pickup_lat), 4326)::geography));
create index if not exists idx_ride_requests_dropoff_geography_gist
    on commute.ride_requests using gist ((st_setsrid(st_makepoint(dropoff_lon, dropoff_lat), 4326)::geography));
create index if not exists idx_route_matches_ride_request_id on commute.route_matches (ride_request_id);

-- Highly optimized, parameterized matching query for passengers against a
-- driver's route. The route LineString is cast to geography and the requested
-- ST_DWithin predicate computes the 500-meter radius on the Earth's surface.
create or replace function commute.find_route_passenger_matches(
    p_driver_route_id uuid default null,
    p_window_start timestamptz default now(),
    p_window_duration interval default interval '2 hours'
)
returns table (
    route_id uuid,
    driver_id uuid,
    ride_request_id uuid,
    passenger_id uuid,
    departure_time timestamptz,
    available_seats integer,
    pickup_distance_meters double precision,
    dropoff_distance_meters double precision
)
language sql
stable
as $$
    select
        r.id as route_id,
        r.driver_id,
        rr.id as ride_request_id,
        rr.passenger_id,
        r.departure_time,
        r.available_seats,
        st_distance(
            r.route_path::geography,
            st_setsrid(st_makepoint(rr.pickup_lon, rr.pickup_lat), 4326)::geography
        ) as pickup_distance_meters,
        st_distance(
            r.route_path::geography,
            st_setsrid(st_makepoint(rr.dropoff_lon, rr.dropoff_lat), 4326)::geography
        ) as dropoff_distance_meters
    from commute.driver_routes r
    join commute.ride_requests rr
        on rr.status = 'open'
       and rr.seats_requested <= r.available_seats
       and (rr.earliest_departure is null or rr.earliest_departure <= r.departure_time)
       and (rr.latest_departure is null or rr.latest_departure >= r.departure_time)
    where r.status = 'scheduled'
      and r.available_seats > 0
      and (p_driver_route_id is null or r.id = p_driver_route_id)
      and r.departure_time >= p_window_start
      and r.departure_time < p_window_start + p_window_duration
      and st_dwithin(
            r.route_path::geography,
            st_setsrid(st_makepoint(rr.pickup_lon, rr.pickup_lat), 4326)::geography,
            500
          )
      and st_dwithin(
            r.route_path::geography,
            st_setsrid(st_makepoint(rr.dropoff_lon, rr.dropoff_lat), 4326)::geography,
            500
          )
    order by r.departure_time asc, pickup_distance_meters asc, dropoff_distance_meters asc;
$$;
