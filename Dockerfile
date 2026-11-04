FROM golang:1.27-alpine AS build

RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jdoc .

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/jdoc /jdoc

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/jdoc"]
CMD ["-listen", ":8080"]
