FROM alpine:latest
WORKDIR /app
COPY server .
COPY mcproxy-linux-amd64 .
COPY static/ static/
RUN chmod +x mcproxy-linux-amd64 server
EXPOSE 8080
CMD ["./server"]
