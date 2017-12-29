# Lastfluss

Flow monitoring for peering/transit interfaces.


## What is Lastfluss

Lastfluss was originally devised as a replacement for the good old AS-Stats tool, but with less Perl and more flexibility.

It uses netflow or ipfix data to monitor traffic on your peering and transit interfaces, so you can see to and from which ASNs your traffic is flowing over which interface.

Currently, the flow importer is written in Go and uses [tflow2](https://github.com/taktv6/tflow2). The data is then stored in a simple PostgreSQL or TimescaleDB database. You can display the data with grafana (dashboard examples included) or any way you want.


## Installation

You can either run a normal PostgreSQL DB, or upgrade the table to a TimescaleDB for better performance. If you don't use TimescaleDB, you can simply skip the related steps.

1. Install PostgreSQL and TimescaleDB
2. Create a user and database for lastfluss
3. Create the TimescaleDB hypertable
4. Install the lastfluss flow importer
5. Configure your installation
6. Start the importer


### Install PostgreSQL and TimescaleDB

Please refer to their installation instructions. Don't forget to include this in your `postgresql.conf` to load TimescaleDB.

```
shared_preload_libraries = 'timescaledb'
```


### Create a user and database for lastfluss

For simplicity, you can name them both `lastfluss`.


### Create the TimescaleDB hypertable

Connect to your database (`\c lastfluss`), create the table and then create the hypertable.
```
CREATE TABLE IF NOT EXISTS traffic (
    id BIGSERIAL,
    time TIMESTAMPTZ NOT NULL,
    bandwidth REAL NOT NULL,
    iface TEXT,
    ingress BOOLEAN NOT NULL,
    remote_asn BIGINT,
    local_asn BIGINT,
    PRIMARY KEY(id, time)
);
CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;
SELECT create_hypertable('traffic', 'time');

```

If you're not using TimescaleDB, the importer will create the table for you, if it doesn't exist.


### Install the lastfluss flow importer

Get the requirements:
```
go get -u github.com/google/tflow2/nfserver
go get -u github.com/lib/pq
go get -u gopkg.in/yaml.v2
```

Compile the importer:
```
go build -o importer src/importer.go
```


### Configure your installation

There are example configuration files for the settings and your netflow interfaces in the examples directory.
```
examples/
├── interfaces.yaml.example
└── settings.yaml.example
```

You can copy these files to the working directory of your importer and modify them.


### Start the importer

You can now start the importer.
```
./importer
```

An example systemd unit file is included.


### Install and configure Grafana

Please refer to the Grafana installation guide.

In the Grafana UI, you need to configure your PostgreSQL DB as datasource. A few example dashboards are included, sice it can be tricky to get several things right in Grafana, like group stacking for in/egress traffic.


## Limitations

Please that the database can grow pretty quickly, depending on your traffic, the granulatiry of your flow sampling and the configured intervals. Also, a large database may slow queries down.

TimescaleDB does not ([yet](https://github.com/timescale/timescaledb/issues/350)) offer automatic rollup or retention policies.
