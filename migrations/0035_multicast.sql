-- +migrate Up
create table multicast_group (
    id uuid primary key,
    created_at timestamp with time zone not null,
    updated_at timestamp with time zone not null,
    name varchar(100) not null,
    service_profile_id uuid not null references service_profile,
    mc_app_s_key bytea
);

create index idx_multicast_group_service_profile_id on multicast_group(service_profile_id);

create table device_multicast_group (
    dev_eui bytea references device on delete cascade,
    multicast_group_id uuid references multicast_group on delete cascade,
    created_at timestamp with time zone not null,

    primary key(multicast_group_id, dev_eui)
);

-- +migrate Down
drop table device_multicast_group;

drop index idx_multicast_group_service_profile_id;

drop table multicast_group;
