#!/bin/sh
# Runs once, when the pgdata volume is first initialised (docker-entrypoint-initdb.d).
# Two databases, two owners: the backend and MLflow cannot read each other's data (ADR 0010).
set -eu
# psql :'var' quotes the passwords, so no value can break out of the SQL.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  -v jr_pw="$JOBRADAR_DB_PASSWORD" -v ml_pw="$MLFLOW_DB_PASSWORD" <<'SQL'
CREATE ROLE jobradar LOGIN PASSWORD :'jr_pw';
CREATE DATABASE jobradar OWNER jobradar;
REVOKE CONNECT ON DATABASE jobradar FROM PUBLIC;
CREATE ROLE mlflow LOGIN PASSWORD :'ml_pw';
CREATE DATABASE mlflow OWNER mlflow;
REVOKE CONNECT ON DATABASE mlflow FROM PUBLIC;
SQL
