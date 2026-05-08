# syntax=docker/dockerfile:1.7

FROM python:3.12-slim AS builder
WORKDIR /src
RUN pip install --no-cache-dir hatchling
COPY pyproject.toml ./
COPY src ./src
RUN pip install --no-cache-dir --target /out . && \
    pip install --no-cache-dir --target /out scikit-learn pandas numpy clickhouse-connect joblib

FROM python:3.12-slim
ENV PYTHONPATH=/app
WORKDIR /app
COPY --from=builder /out /app
RUN useradd --create-home --uid 1000 bamboo
USER bamboo
ENTRYPOINT ["python", "-m", "anomaly.cli"]
CMD ["--help"]
