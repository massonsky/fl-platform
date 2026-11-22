FROM fl-platform/ml-base:mvp
WORKDIR /app
COPY services/ml-client/src /app/src
ENV PYTHONPATH=/app/src:/app/src/fl_client/gen PYTHONUNBUFFERED=1
CMD ["python", "-m", "fl_client.runtime.agent"]
