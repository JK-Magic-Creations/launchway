package engine

// Schema 49, the inventory of fleets of hosts (virtual machines and bare
// metal) in an Airfield. It is number 49, after configuration as code (48).

// schemaFleet is the schema that brought fleet inventories.
const schemaFleet = 49

func init() {
	if len(migrations) != schemaFleet-1 {
		panic("the fleet migration (49) must follow migrations 1 to 48")
	}
	migrations = append(migrations, fleetMigration)
}

// 49: fleet_hosts, one row per host of an Airfield's fleet: where SSH
//
//	answers, the host's pinned public key, where the service answers (for the
//	health check), the load balancer's name for it, and its group (for a
//	Pathfinder: stable or new). The key used to reach the hosts is not here:
//	the Airfield names a secret by reference. Compatibility: additive: one new
//	table, ignored by an older server. Downgrade: nothing to undo.
var fleetMigration = migration{additive, `
CREATE TABLE fleet_hosts (
  environment TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  address     TEXT    NOT NULL,
  ssh_user    TEXT    NOT NULL DEFAULT 'deploy',
  host_key    TEXT    NOT NULL,
  service     TEXT    NOT NULL,
  member      TEXT    NOT NULL DEFAULT '',
  group_name  TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (environment, name)
);
`}
