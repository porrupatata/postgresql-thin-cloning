<div align="center">
  <strong>:zap: Blazing-fast cloning of PostgreSQL databases :elephant:</strong><br>
  Thin clones of PostgreSQL to build powerful development, test, QA, and staging environments.<br>
  Available for any PostgreSQL
</div>
<br /><br /><br />
DISCLAIMER:<br />
This is an experimental fork from Database Lab Engine and it is only intended for testing purposes.<br />
Some features have been disabled. And some other features may not work. <br />
In order to find the original working version or for contributions, please, visit https://github.com/postgres-ai/database-lab-engine



<br />


---


## How it works
Thin cloning is fast because it uses [Copy-on-Write (CoW)](https://en.wikipedia.org/wiki/Copy-on-write#In_computer_storage). DLE supports two technologies to enable CoW and thin cloning: [ZFS](https://en.wikipedia.org/wiki/ZFS) (default) and [LVM](https://en.wikipedia.org/wiki/Logical_Volume_Manager_(Linux)).

With ZFS, Database Lab Engine periodically creates a new snapshot of the data directory and maintains a set of snapshots, cleaning up old and unused ones. When requesting a new clone, users can choose which snapshot to use.

## Features
- Blazing-fast cloning of Postgres databases – a few seconds to create a new clone ready to accept connections and queries, regardless of database size.
- The theoretical maximum number of snapshots and clones is 2<sup>64</sup> ([ZFS](https://en.wikipedia.org/wiki/ZFS), default).
- The theoretical maximum size of PostgreSQL data directory: 256 quadrillion zebibytes, or 2<sup>128</sup> bytes ([ZFS](https://en.wikipedia.org/wiki/ZFS), default).
- PostgreSQL major versions supported: 9.6–14.
- Two technologies are supported to enable thin cloning ([CoW](https://en.wikipedia.org/wiki/Copy-on-write)): [ZFS](https://en.wikipedia.org/wiki/ZFS) and [LVM](https://en.wikipedia.org/wiki/Logical_Volume_Manager_(Linux)).
- All components are packaged in Docker containers.
- API and CLI to automate the work with DLE snapshots and clones.
- Initial data provisioning can be done at either the physical (pg_basebackup, backup / archiving tools such as WAL-G or pgBackRest) or logical (dump/restore directly from the source or from files stored at AWS S3) level.
- For logical mode, partial data retrieval is supported (specific databases, specific tables).
- For physical mode, a continuously updated state is supported ("sync container"), making DLE a specialized version of standby Postgres.
- For logical mode, periodic full refresh is supported, automated, and controlled by DLE. It is possible to use multiple disks containing different versions of the database, so full refresh won't require downtime.
- Fast Point in Time Recovery (PITR) to the points available in DLE snapshots.
- Unused clones are automatically deleted.
- "Deletion protection" flag can be used to block automatic or manual deletion of clones.
- Persistent clones: clones survive DLE restarts (including full VM reboots).
- The "reset" command can be used to switch to a different version of data.
- SSH port forwarding for API and Postgres connections.
- Docker container config parameters can be specified in the DLE config.
- Resource usage quotas for clones: CPU, RAM (container quotas, supported by Docker)



## License
DLE source code is licensed under the OSI-approved open source license GNU Affero General Public License version 3 (AGPLv3).



