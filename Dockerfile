
FROM alpine:latest AS production

WORKDIR /root/
RUN apk --no-cache add ca-certificates
COPY  ./build/linux/lora-app-server .
ENTRYPOINT ["./lora-app-server"]
