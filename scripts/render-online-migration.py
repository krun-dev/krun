#!/usr/bin/env python3
"""Render a reviewed online migration and its ledger update as a psql script."""
import argparse
import hashlib
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
MIGRATIONS = ROOT / "internal/storage/migrations"
NAME = re.compile(r"^(\d{4})_[a-z0-9_]+\.sql$")
ONLINE = re.compile(r"^(\d{4})_online_from_(\d{4})_[a-z0-9_]+\.sql$")


def render(target: str) -> str:
    files = sorted(MIGRATIONS.glob("*.sql"))
    for number, path in enumerate(files, 1):
        match = NAME.fullmatch(path.name)
        if not match or int(match.group(1)) != number:
            raise ValueError("migration files must have consecutive versions")
    names = [path.name for path in files]
    if target not in names:
        raise ValueError("target must be a migration in internal/storage/migrations")
    path = files[names.index(target)]
    match = ONLINE.fullmatch(path.name)
    if not match or int(match.group(2)) < 1 or int(match.group(2)) >= int(match.group(1)):
        raise ValueError("target must declare online compatibility in its filename")
    version = int(match.group(1))
    previous = files[:version - 1]
    expected = ",\n".join(
        "(%d, '%s', '%s')" % (i, p.name, hashlib.sha256(p.read_bytes()).hexdigest())
        for i, p in enumerate(previous, 1)
    )
    checksum = hashlib.sha256(path.read_bytes()).hexdigest()
    sql = path.read_text(encoding="utf-8")
    # The history guard and DDL execute atomically. Lock matches krun-server migrate.
    return f'''-- Review the migration, compatibility and database target before running.
\\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
LOCK TABLE krun_schema_migrations IN SHARE ROW EXCLUSIVE MODE;
DO $krun_guard$
BEGIN
  IF EXISTS (
    (SELECT version, name, checksum FROM krun_schema_migrations
     EXCEPT SELECT * FROM (VALUES {expected}) AS expected(version, name, checksum))
    UNION ALL
    (SELECT * FROM (VALUES {expected}) AS expected(version, name, checksum)
     EXCEPT SELECT version, name, checksum FROM krun_schema_migrations)
  ) THEN
    RAISE EXCEPTION 'migration history does not match the required predecessor';
  END IF;
END
$krun_guard$;
{sql}
INSERT INTO krun_schema_migrations(version,name,checksum)
VALUES ({version}, '{path.name}', '{checksum}');
COMMIT;
'''


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("migration", help="online migration filename")
    args = parser.parse_args()
    try:
        sys.stdout.write(render(args.migration))
    except (OSError, ValueError) as exc:
        print(exc, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
