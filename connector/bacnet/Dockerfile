FROM python:3.12-slim

WORKDIR /app

# Install uv for fast dependency resolution
RUN pip install --no-cache-dir uv

COPY pyproject.toml ./
RUN uv pip install --system --no-cache .

COPY bacnet_connector/ bacnet_connector/

ENV PYTHONUNBUFFERED=1

EXPOSE 8080

ENTRYPOINT ["python", "-m", "bacnet_connector.main"]
