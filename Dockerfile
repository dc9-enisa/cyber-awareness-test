# Aplikacja webowa testu „Cyberbezpieczeństwo w codziennej pracy” z logowaniem Microsoft Entra ID.
# Budowa:  docker build -t cyberbezpieczenstwo-test .
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go vet ./... \
 && CGO_ENABLED=0 go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/web-app . \
 && mkdir -p /out/data

# Obraz bez powłoki i menedżera pakietów; zawiera certyfikaty CA (połączenie z login.microsoftonline.com).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/web-app /app/web-app
# katalog wyników należy do użytkownika nonroot (65532) – nowy wolumen przejmuje te uprawnienia
COPY --from=build --chown=65532:65532 /out/data /data
ENV LISTEN=:8080 DATA_DIR=/data
EXPOSE 8080
VOLUME ["/data"]
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/app/web-app", "healthcheck"]
ENTRYPOINT ["/app/web-app"]
