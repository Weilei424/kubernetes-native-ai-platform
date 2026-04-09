# infra/docker/model-loader/Dockerfile
FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY loader.py .

ENTRYPOINT ["python", "loader.py"]
