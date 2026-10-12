FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
RUN mkdir -p /data && chown 10001:10001 /data
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /trailvault .
FROM scratch
COPY --from=build /trailvault /trailvault
COPY --from=build --chown=10001:10001 /data /data
USER 10001:10001
ENV TRAIL_ADDR=0.0.0.0:8080 TRAIL_DATA=/data/events.jsonl
EXPOSE 8080
ENTRYPOINT ["/trailvault"]
