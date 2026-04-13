# training-runtime/examples/resnet50/Dockerfile
# Pre-download ResNet50 weights at build time to avoid demo delays.
FROM platform/training-base:latest
RUN python -c "import torchvision.models as m; m.resnet50(weights=m.ResNet50_Weights.DEFAULT)"
COPY export.py /app/export.py
ENTRYPOINT ["python", "/app/export.py"]
