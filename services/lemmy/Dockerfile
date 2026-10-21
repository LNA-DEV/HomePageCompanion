# The Lemmy service (services/lemmy). Built with the repository as its
# context, for the Python SDK next to it:
#
#   services:
#     lemmy:
#       from: path:.                                  # or git+https://github.com/LNA-DEV/HomePageCompanion@<tag>
#       dockerfile: services/lemmy/Dockerfile
FROM python:3.12-slim
WORKDIR /app
COPY sdk/python/companion_service /app/companion_service
COPY services/lemmy/lemmy.py /app/lemmy.py
COPY services/lemmy/ui /app/ui
# Not root; the root file system is read-only, so no bytecode files.
USER 10001:10001
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
EXPOSE 8080
ENTRYPOINT ["python3", "/app/lemmy.py"]
