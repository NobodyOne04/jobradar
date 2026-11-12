# The official image lacks a Postgres driver for the backend store (ADR 0010).
# psycopg 3: SQLAlchemy 2.1 maps postgresql:// to it, not to psycopg2.
FROM ghcr.io/mlflow/mlflow:v3.18.0
RUN pip install --no-cache-dir "psycopg[binary]==3.3.9"
