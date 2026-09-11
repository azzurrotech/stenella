FROM golang:1.20-alpine

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .

RUN go build -o stenella-server ./main.go

EXPOSE 8084

CMD ["/app/stenella-server"]
