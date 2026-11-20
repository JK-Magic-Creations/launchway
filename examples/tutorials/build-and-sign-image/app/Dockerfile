# A tiny program: it prints a greeting and the version it was built as.
FROM docker.io/library/alpine:3.22
ARG VERSION=dev
COPY greeting.txt /app/greeting.txt
RUN echo "$VERSION" > /app/version.txt
USER 1000:1000
CMD ["sh", "-c", "cat /app/greeting.txt; echo \"version $(cat /app/version.txt)\""]
